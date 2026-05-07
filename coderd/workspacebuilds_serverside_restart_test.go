package coderd_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/coder/coder/v2/coderd/coderdtest"
	"github.com/coder/coder/v2/coderd/database/dbauthz"
	"github.com/coder/coder/v2/codersdk"
	"github.com/coder/coder/v2/provisioner/echo"
	"github.com/coder/coder/v2/provisionersdk/proto"
	"github.com/coder/coder/v2/testutil"
)

func TestPostWorkspaceBuildsOnSuccessRestart(t *testing.T) {
	t.Parallel()

	const paramName = "foo"

	// Given: a running workspace with an existing rich parameter value.
	deploymentValues := coderdtest.DeploymentValues(t)
	deploymentValues.EnableTerraformDebugMode = true
	client, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{
		IncludeProvisionerDaemon: true,
		DeploymentValues:         deploymentValues,
	})
	first := coderdtest.CreateFirstUser(t, client)
	version := coderdtest.CreateTemplateVersion(t, client, first.OrganizationID,
		echoResponsesWithRichParameter(paramName, echoResponseOptions{
			blockStopApply: false,
		}),
	)
	coderdtest.AwaitTemplateVersionJobCompleted(t, client, version.ID)
	template := coderdtest.CreateTemplate(t, client, first.OrganizationID, version.ID)
	workspace := coderdtest.CreateWorkspace(t, client, template.ID, func(request *codersdk.CreateWorkspaceRequest) {
		request.RichParameterValues = []codersdk.WorkspaceBuildParameter{
			{Name: paramName, Value: "bar"},
		}
	})
	initialBuild := coderdtest.AwaitWorkspaceBuildJobCompleted(t, client, workspace.LatestBuild.ID)
	require.Equal(t, codersdk.WorkspaceStatusRunning, initialBuild.Status)

	// When: a stop build is created with an on_success start build.
	ctx := testutil.Context(t, testutil.WaitLong)
	user, err := client.User(ctx, codersdk.Me)
	require.NoError(t, err)

	stopBuild, err := client.CreateWorkspaceBuild(ctx, workspace.ID, codersdk.CreateWorkspaceBuildRequest{
		Transition: codersdk.WorkspaceTransitionStop,
		Reason:     codersdk.CreateWorkspaceBuildReasonCLI,
		LogLevel:   codersdk.ProvisionerLogLevelDebug,
		OnSuccess: &codersdk.CreateWorkspaceBuildOnSuccessRequest{
			Transition:        codersdk.WorkspaceTransitionStart,
			TemplateVersionID: template.ActiveVersionID,
			RichParameterValues: []codersdk.WorkspaceBuildParameter{
				{Name: paramName, Value: "baz"},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, codersdk.WorkspaceTransitionStop, stopBuild.Transition)
	require.Equal(t, codersdk.BuildReasonCLI, stopBuild.Reason)

	// Then: the server persists the child start build intent.
	orchestration, err := db.GetWorkspaceBuildOrchestrationByParentBuildID(dbauthz.AsSystemRestricted(ctx), stopBuild.ID)
	require.NoError(t, err)
	require.Equal(t, codersdk.WorkspaceTransitionStart, codersdk.WorkspaceTransition(orchestration.ChildTransition))
	require.True(t, orchestration.ChildTemplateVersionID.Valid)
	require.Equal(t, template.ActiveVersionID, orchestration.ChildTemplateVersionID.UUID)
	require.False(t, orchestration.ChildTemplateVersionPresetID.Valid)
	require.Equal(t, string(codersdk.ProvisionerLogLevelDebug), orchestration.ChildLogLevel)
	require.True(t, orchestration.ChildReason.Valid)
	require.Equal(t, codersdk.BuildReasonCLI, codersdk.BuildReason(orchestration.ChildReason.BuildReason))

	var childRichParameterValues []codersdk.WorkspaceBuildParameter
	require.NoError(t, json.Unmarshal(orchestration.ChildRichParameterValues, &childRichParameterValues))
	require.ElementsMatch(t, []codersdk.WorkspaceBuildParameter{
		{Name: paramName, Value: "baz"},
	}, childRichParameterValues)

	// Then: the returned parent stop build completes successfully.
	stopBuild = coderdtest.AwaitWorkspaceBuildJobCompleted(t, client, stopBuild.ID)
	require.Equal(t, codersdk.ProvisionerJobSucceeded, stopBuild.Job.Status)
	require.Equal(t, codersdk.WorkspaceStatusStopped, stopBuild.Status)

	// Then: the server creates and completes the child start build.
	var childBuild codersdk.WorkspaceBuild
	require.Eventually(t, func() bool {
		childBuild, err = client.WorkspaceBuildByUsernameAndWorkspaceNameAndBuildNumber(
			ctx,
			user.Username,
			workspace.Name,
			strconv.FormatInt(int64(stopBuild.BuildNumber+1), 10),
		)
		return err == nil &&
			childBuild.Transition == codersdk.WorkspaceTransitionStart
	}, testutil.WaitMedium, testutil.IntervalFast)

	childBuild = coderdtest.AwaitWorkspaceBuildJobCompleted(t, client, childBuild.ID)
	require.Equal(t, codersdk.ProvisionerJobSucceeded, childBuild.Job.Status)
	require.Equal(t, codersdk.WorkspaceStatusRunning, childBuild.Status)
	require.Equal(t, codersdk.BuildReasonCLI, childBuild.Reason)
	require.Equal(t, template.ActiveVersionID, childBuild.TemplateVersionID)

	// Then: the child build uses the on_success parameter values.
	params, err := client.WorkspaceBuildParameters(ctx, childBuild.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []codersdk.WorkspaceBuildParameter{
		{Name: paramName, Value: "baz"},
	}, params)
}

func TestPostWorkspaceBuildsOnSuccessValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request codersdk.CreateWorkspaceBuildRequest
	}{
		{
			name: "ParentMustBeStop",
			request: codersdk.CreateWorkspaceBuildRequest{
				Transition: codersdk.WorkspaceTransitionStart,
				OnSuccess: &codersdk.CreateWorkspaceBuildOnSuccessRequest{
					Transition: codersdk.WorkspaceTransitionStart,
				},
			},
		},
		{
			name: "ChildMustBeStart",
			request: codersdk.CreateWorkspaceBuildRequest{
				Transition: codersdk.WorkspaceTransitionStop,
				OnSuccess: &codersdk.CreateWorkspaceBuildOnSuccessRequest{
					Transition: codersdk.WorkspaceTransitionStop,
				},
			},
		},
		{
			name: "ParentDryRunRejected",
			request: codersdk.CreateWorkspaceBuildRequest{
				Transition: codersdk.WorkspaceTransitionStop,
				DryRun:     true,
				OnSuccess: &codersdk.CreateWorkspaceBuildOnSuccessRequest{
					Transition: codersdk.WorkspaceTransitionStart,
				},
			},
		},
		{
			name: "ParentOrphanRejected",
			request: codersdk.CreateWorkspaceBuildRequest{
				Transition: codersdk.WorkspaceTransitionStop,
				Orphan:     true,
				OnSuccess: &codersdk.CreateWorkspaceBuildOnSuccessRequest{
					Transition: codersdk.WorkspaceTransitionStart,
				},
			},
		},
		{
			name: "ParentProvisionerStateRejected",
			request: codersdk.CreateWorkspaceBuildRequest{
				Transition:       codersdk.WorkspaceTransitionStop,
				ProvisionerState: []byte("state"),
				OnSuccess: &codersdk.CreateWorkspaceBuildOnSuccessRequest{
					Transition: codersdk.WorkspaceTransitionStart,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Given: a running workspace.
			client := coderdtest.New(t, &coderdtest.Options{IncludeProvisionerDaemon: true})
			first := coderdtest.CreateFirstUser(t, client)
			version := coderdtest.CreateTemplateVersion(t, client, first.OrganizationID, nil)
			coderdtest.AwaitTemplateVersionJobCompleted(t, client, version.ID)
			template := coderdtest.CreateTemplate(t, client, first.OrganizationID, version.ID)
			workspace := coderdtest.CreateWorkspace(t, client, template.ID)
			coderdtest.AwaitWorkspaceBuildJobCompleted(t, client, workspace.LatestBuild.ID)

			// When: an invalid on_success request is posted.
			_, err := client.CreateWorkspaceBuild(testutil.Context(t, testutil.WaitLong), workspace.ID, tt.request)
			require.Error(t, err)

			// Then: the API rejects the request before creating a build.
			var apiErr *codersdk.Error
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
		})
	}
}

func TestPostWorkspaceBuildsOnSuccessParentCanceled(t *testing.T) {
	t.Parallel()

	// Given: a running workspace whose stop apply will block.
	client := coderdtest.New(t, &coderdtest.Options{IncludeProvisionerDaemon: true})
	first := coderdtest.CreateFirstUser(t, client)
	version := coderdtest.CreateTemplateVersion(t, client, first.OrganizationID,
		echoResponsesWithRichParameter("foo", echoResponseOptions{
			blockStopApply: true,
		}),
	)
	coderdtest.AwaitTemplateVersionJobCompleted(t, client, version.ID)
	template := coderdtest.CreateTemplate(t, client, first.OrganizationID, version.ID)
	workspace := coderdtest.CreateWorkspace(t, client, template.ID)
	initialBuild := coderdtest.AwaitWorkspaceBuildJobCompleted(t, client, workspace.LatestBuild.ID)
	require.Equal(t, codersdk.WorkspaceStatusRunning, initialBuild.Status)

	// When: a stop build is created with an on_success start build.
	ctx := testutil.Context(t, testutil.WaitLong)
	user, err := client.User(ctx, codersdk.Me)
	require.NoError(t, err)

	stopBuild, err := client.CreateWorkspaceBuild(ctx, workspace.ID, codersdk.CreateWorkspaceBuildRequest{
		Transition: codersdk.WorkspaceTransitionStop,
		OnSuccess: &codersdk.CreateWorkspaceBuildOnSuccessRequest{
			Transition:        codersdk.WorkspaceTransitionStart,
			TemplateVersionID: template.ActiveVersionID,
		},
	})
	require.NoError(t, err)
	require.Equal(t, codersdk.WorkspaceTransitionStop, stopBuild.Transition)
	require.Equal(t, codersdk.BuildReasonInitiator, stopBuild.Reason)

	// When: the parent stop build starts running and is canceled.
	require.Eventually(t, func() bool {
		var err error
		stopBuild, err = client.WorkspaceBuild(ctx, stopBuild.ID)
		return err == nil &&
			stopBuild.Job.Status == codersdk.ProvisionerJobRunning
	}, testutil.WaitShort, testutil.IntervalFast)

	require.NoError(t, client.CancelWorkspaceBuild(ctx, stopBuild.ID, codersdk.CancelWorkspaceBuildParams{}))
	require.Eventually(t, func() bool {
		var err error
		stopBuild, err = client.WorkspaceBuild(ctx, stopBuild.ID)
		if err != nil {
			return false
		}
		return stopBuild.Job.Status == codersdk.ProvisionerJobCanceled ||
			(stopBuild.Job.Status == codersdk.ProvisionerJobFailed &&
				stopBuild.Job.Error == "canceled")
	}, testutil.WaitShort, testutil.IntervalFast)

	// Then: the server does not create the child start build.
	require.Never(t, func() bool {
		_, err := client.WorkspaceBuildByUsernameAndWorkspaceNameAndBuildNumber(
			ctx,
			user.Username,
			workspace.Name,
			strconv.FormatInt(int64(stopBuild.BuildNumber+1), 10),
		)
		return err == nil
	}, testutil.WaitShort, testutil.IntervalFast)
}

type echoResponseOptions struct {
	blockStopApply bool
}

func echoResponsesWithRichParameter(paramName string, options echoResponseOptions) *echo.Responses {
	responses := &echo.Responses{
		Parse:         echo.ParseComplete,
		ProvisionInit: echo.InitComplete,
		ProvisionGraph: []*proto.Response{{
			Type: &proto.Response_Graph{
				Graph: &proto.GraphComplete{
					Parameters: []*proto.RichParameter{{
						Name:         paramName,
						Type:         "string",
						DefaultValue: "bar",
						Mutable:      true,
						FormType:     proto.ParameterFormType_INPUT,
					}},
				},
			},
		}},
		ProvisionPlan:  echo.PlanComplete,
		ProvisionApply: echo.ApplyComplete,
	}
	if options.blockStopApply {
		responses.ProvisionApplyMap = map[proto.WorkspaceTransition][]*proto.Response{
			proto.WorkspaceTransition_START: echo.ApplyComplete,
			proto.WorkspaceTransition_STOP: {{
				Type: &proto.Response_Log{
					Log: &proto.Log{},
				},
			}},
		}
	}
	return responses
}
