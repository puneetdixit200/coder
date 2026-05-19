import { render, screen, waitFor } from "@testing-library/react";
import {
	type FC,
	type ReactNode,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { QueryClientProvider } from "react-query";
import { describe, expect, it } from "vitest";
import type * as TypesGen from "#/api/typesGenerated";
import { createTestQueryClient } from "#/testHelpers/renderHelpers";
import { ChatMessageInput, type ChatMessageInputRef } from "./ChatMessageInput";
import { personalSkillTriggerText } from "./PersonalSkillsTriggerMenu";

const renderWithQueryClient = (children: ReactNode) => {
	const queryClient = createTestQueryClient();

	return render(
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>,
	);
};

const InitialValueHarness: FC<{ initialValue: string }> = ({
	initialValue,
}) => {
	const inputRef = useRef<ChatMessageInputRef>(null);
	const [observedValue, setObservedValue] = useState("");

	useLayoutEffect(() => {
		setObservedValue(inputRef.current?.getValue() ?? "");
	}, []);

	return (
		<>
			<div data-testid="observed-value">{observedValue}</div>
			<ChatMessageInput
				ref={inputRef}
				initialValue={initialValue}
				aria-label="Chat message input"
			/>
		</>
	);
};

const QueuedReplacementHarness: FC<{
	initialValue: string;
	replacementValue: string;
}> = ({ initialValue, replacementValue }) => {
	const inputRef = useRef<ChatMessageInputRef>(null);
	const [observedValue, setObservedValue] = useState("");

	useLayoutEffect(() => {
		inputRef.current?.setValue(replacementValue);
		setObservedValue(inputRef.current?.getValue() ?? "");
	}, [replacementValue]);

	return (
		<>
			<div data-testid="observed-value">{observedValue}</div>
			<ChatMessageInput
				ref={inputRef}
				initialValue={initialValue}
				aria-label="Chat message input"
			/>
		</>
	);
};

describe("ChatMessageInput", () => {
	it("returns the initial draft before the editor visually hydrates", async () => {
		renderWithQueryClient(
			<InitialValueHarness initialValue="persisted draft" />,
		);

		expect(screen.getByTestId("observed-value")).toHaveTextContent(
			"persisted draft",
		);
		await waitFor(() => {
			expect(screen.getByTestId("chat-message-input").textContent).toBe(
				"persisted draft",
			);
		});
	});

	it("queues setValue calls made before the editor is ready", async () => {
		renderWithQueryClient(
			<QueuedReplacementHarness
				initialValue="persisted draft"
				replacementValue="queued replacement"
			/>,
		);

		expect(screen.getByTestId("observed-value")).toHaveTextContent(
			"queued replacement",
		);
		await waitFor(() => {
			expect(screen.getByTestId("chat-message-input").textContent).toBe(
				"queued replacement",
			);
		});
	});

	it("personalSkillTriggerText returns slash-prefixed name only", () => {
		const skill: TypesGen.UserSkillMetadata = {
			id: "skill-reviewer",
			name: "reviewer",
			description: "Review changed files and suggest fixes.",
			created_at: "2026-05-08T00:00:00Z",
			updated_at: "2026-05-08T00:00:00Z",
		};

		expect(personalSkillTriggerText(skill)).toBe("/reviewer");
	});

	it("returns updated content even without an external onChange prop", async () => {
		const inputRef = { current: null as ChatMessageInputRef | null };
		renderWithQueryClient(
			<ChatMessageInput ref={inputRef} aria-label="Chat message input" />,
		);

		await waitFor(() => {
			expect(inputRef.current).not.toBeNull();
		});

		inputRef.current?.insertText("typed content");

		await waitFor(() => {
			expect(inputRef.current?.getValue()).toBe("typed content");
		});
	});
});
