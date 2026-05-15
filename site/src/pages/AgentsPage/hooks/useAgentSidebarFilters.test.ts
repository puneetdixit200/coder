import { act, waitFor } from "@testing-library/react";
import { renderHookWithAuth } from "#/testHelpers/hooks";
import {
	type AgentSidebarFilters,
	useAgentSidebarFilters,
} from "./useAgentSidebarFilters";

describe(useAgentSidebarFilters.name, () => {
	const archivedFilters: AgentSidebarFilters = {
		archived: "archived",
		groupBy: "chat_status",
		prStatuses: ["draft", "merged"],
		unreadOnly: true,
	};

	it("returns defaults for /agents", async () => {
		const { result } = await renderHookWithAuth(
			() => useAgentSidebarFilters(),
			{
				routingOptions: { path: "/agents", route: "/agents" },
			},
		);

		expect(result.current[0]).toEqual({
			archived: "active",
			groupBy: "date",
			prStatuses: [],
			unreadOnly: false,
		});
	});

	it("parses archived, group_by, pr_status, and chat_status from the URL", async () => {
		const { result } = await renderHookWithAuth(
			() => useAgentSidebarFilters(),
			{
				routingOptions: {
					path: "/agents",
					route:
						"/agents?archived=archived&group_by=chat_status&pr_status=open,draft,closed&chat_status=unread",
				},
			},
		);

		expect(result.current[0]).toEqual({
			archived: "archived",
			groupBy: "chat_status",
			prStatuses: ["draft", "open", "closed"],
			unreadOnly: true,
		});
	});

	it("drops invalid pr_status values and canonicalizes order", async () => {
		const { result } = await renderHookWithAuth(
			() => useAgentSidebarFilters(),
			{
				routingOptions: {
					path: "/agents",
					route:
						"/agents?pr_status=merged,bogus,DRAFT,merged&pr_status=open,review,closed,draft",
				},
			},
		);

		expect(result.current[0].prStatuses).toEqual([
			"draft",
			"open",
			"merged",
			"closed",
		]);
	});

	it("omits default values when writing filters", async () => {
		const { result, getLocationSnapshot } = await renderHookWithAuth(
			() => useAgentSidebarFilters(),
			{
				routingOptions: {
					path: "/agents",
					route:
						"/agents?archived=archived&group_by=chat_status&pr_status=draft&chat_status=unread",
				},
			},
		);

		act(() => {
			result.current[1]({
				archived: "active",
				groupBy: "date",
				prStatuses: [],
				unreadOnly: false,
			});
		});
		await waitFor(() =>
			expect(result.current[0]).toEqual({
				archived: "active",
				groupBy: "date",
				prStatuses: [],
				unreadOnly: false,
			}),
		);

		const { search } = getLocationSnapshot();
		expect(search.get("archived")).toEqual(null);
		expect(search.get("group_by")).toEqual(null);
		expect(search.get("pr_status")).toEqual(null);
		expect(search.get("chat_status")).toEqual(null);
	});

	it("clearAll resets the URL to the canonical default", async () => {
		const { result, getLocationSnapshot } = await renderHookWithAuth(
			() => useAgentSidebarFilters(),
			{
				routingOptions: {
					path: "/agents",
					route:
						"/agents?archived=archived&group_by=chat_status&pr_status=draft,merged&chat_status=unread",
				},
			},
		);

		act(() => {
			result.current[2]();
		});
		await waitFor(() =>
			expect(result.current[0]).toEqual({
				archived: "active",
				groupBy: "date",
				prStatuses: [],
				unreadOnly: false,
			}),
		);

		const snapshot = getLocationSnapshot();
		expect(snapshot.pathname).toBe("/agents");
		expect(snapshot.search.toString()).toBe("");
	});

	it("preserves unrelated search params when writing filters", async () => {
		const { result, getLocationSnapshot } = await renderHookWithAuth(
			() => useAgentSidebarFilters(),
			{
				routingOptions: {
					path: "/agents",
					route: "/agents?tab=settings&foo=bar&archived=archived",
				},
			},
		);

		act(() => {
			result.current[1](archivedFilters);
		});
		await waitFor(() => expect(result.current[0]).toEqual(archivedFilters));

		const { search } = getLocationSnapshot();
		expect(search.get("tab")).toBe("settings");
		expect(search.get("foo")).toBe("bar");
		expect(search.get("archived")).toBe("archived");
		expect(search.get("group_by")).toBe("chat_status");
		expect(search.get("pr_status")).toBe("draft,merged");
		expect(search.get("chat_status")).toBe("unread");
	});
});
