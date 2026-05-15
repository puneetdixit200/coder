import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import type { AgentSidebarFilters } from "../../hooks/useAgentSidebarFilters";
import { FilterDropdown } from "./FilterDropdown";

const defaultFilters: AgentSidebarFilters = {
	archived: "active",
	groupBy: "date",
	prStatuses: [],
	unreadOnly: false,
};

const appliedFilters: AgentSidebarFilters = {
	archived: "archived",
	groupBy: "chat_status",
	prStatuses: ["draft", "open"],
	unreadOnly: true,
};

const meta: Meta<typeof FilterDropdown> = {
	title: "pages/AgentsPage/FilterDropdown",
	component: FilterDropdown,
	args: {
		filters: defaultFilters,
		onFiltersChange: fn(),
	},
	render: (args) => {
		const [filters, setFilters] = useState(args.filters);
		return (
			<FilterDropdown
				filters={filters}
				onFiltersChange={(nextFilters) => {
					setFilters(nextFilters);
					args.onFiltersChange(nextFilters);
				}}
			/>
		);
	},
};

export default meta;
type Story = StoryObj<typeof FilterDropdown>;

export const OpensFilterPopover: Story = {
	play: async ({ canvasElement }) => {
		const canvas = within(canvasElement);
		const body = within(document.body);

		await userEvent.click(
			canvas.getByRole("button", { name: "Filter agents" }),
		);

		const dialog = await body.findByRole("dialog", { name: "Filter agents" });
		await expect(dialog).toBeInTheDocument();
		await expect(
			within(dialog).getByRole("radiogroup", { name: "Group" }),
		).toBeInTheDocument();
		await expect(
			within(dialog).getByRole("radiogroup", { name: "Archive status" }),
		).toBeInTheDocument();
		await expect(
			within(dialog).getByRole("textbox", { name: "Search filters" }),
		).toBeInTheDocument();
		await expect(
			within(dialog).getByRole("checkbox", { name: "Draft" }),
		).toBeInTheDocument();
		await expect(
			within(dialog).getByRole("checkbox", { name: "Open" }),
		).toBeInTheDocument();
		await expect(
			within(dialog).getByRole("checkbox", { name: "Merged" }),
		).toBeInTheDocument();
		await expect(
			within(dialog).getByRole("checkbox", { name: "Closed" }),
		).toBeInTheDocument();
		await expect(
			within(dialog).getByRole("checkbox", { name: "Unread" }),
		).toBeInTheDocument();
		await expect(
			within(dialog).getByRole("button", { name: "Clear all" }),
		).toBeInTheDocument();
		await expect(
			within(dialog).getByRole("button", { name: "Apply" }),
		).toBeInTheDocument();
	},
};

export const AppliesStagedFilters: Story = {
	args: {
		onFiltersChange: fn(),
	},
	play: async ({ args, canvasElement }) => {
		const canvas = within(canvasElement);
		const body = within(document.body);

		await userEvent.click(
			canvas.getByRole("button", { name: "Filter agents" }),
		);
		const dialog = await body.findByRole("dialog", { name: "Filter agents" });
		await userEvent.click(
			within(dialog).getByRole("radio", { name: "Chat status" }),
		);
		await userEvent.click(
			within(dialog).getByRole("radio", { name: "Archived" }),
		);
		await userEvent.click(
			within(dialog).getByRole("checkbox", { name: "Draft" }),
		);
		await userEvent.click(
			within(dialog).getByRole("checkbox", { name: "Unread" }),
		);

		expect(args.onFiltersChange).not.toHaveBeenCalled();

		await userEvent.click(
			within(dialog).getByRole("button", { name: "Apply" }),
		);

		await expect(args.onFiltersChange).toHaveBeenCalledWith({
			archived: "archived",
			groupBy: "chat_status",
			prStatuses: ["draft"],
			unreadOnly: true,
		});
		await waitFor(() => {
			expect(
				body.queryByRole("dialog", { name: "Filter agents" }),
			).not.toBeInTheDocument();
		});
	},
};

export const ClearAllResetsFilters: Story = {
	args: {
		filters: appliedFilters,
		onFiltersChange: fn(),
	},
	play: async ({ args, canvasElement }) => {
		const canvas = within(canvasElement);
		const body = within(document.body);

		await userEvent.click(
			canvas.getByRole("button", { name: "Filter agents" }),
		);
		const dialog = await body.findByRole("dialog", { name: "Filter agents" });
		await userEvent.click(
			within(dialog).getByRole("button", { name: "Clear all" }),
		);

		await expect(
			within(dialog).getByRole("radio", { name: "Date" }),
		).toBeChecked();
		await expect(
			within(dialog).getByRole("radio", { name: "Active" }),
		).toBeChecked();
		await expect(
			within(dialog).getByRole("checkbox", { name: "Draft" }),
		).not.toBeChecked();
		await expect(
			within(dialog).getByRole("checkbox", { name: "Open" }),
		).not.toBeChecked();
		await expect(
			within(dialog).getByRole("checkbox", { name: "Unread" }),
		).not.toBeChecked();
		expect(args.onFiltersChange).not.toHaveBeenCalled();

		await userEvent.click(
			within(dialog).getByRole("button", { name: "Apply" }),
		);

		await expect(args.onFiltersChange).toHaveBeenCalledWith(defaultFilters);
	},
};

export const SearchFiltersOptions: Story = {
	play: async ({ canvasElement }) => {
		const canvas = within(canvasElement);
		const body = within(document.body);

		await userEvent.click(
			canvas.getByRole("button", { name: "Filter agents" }),
		);
		const dialog = await body.findByRole("dialog", { name: "Filter agents" });
		await userEvent.type(
			within(dialog).getByRole("textbox", { name: "Search filters" }),
			"merged",
		);

		await expect(
			within(dialog).getByRole("checkbox", { name: "Merged" }),
		).toBeVisible();
		expect(
			within(dialog).queryByRole("checkbox", { name: "Draft" }),
		).not.toBeInTheDocument();
		expect(
			within(dialog).queryByRole("radio", { name: "Archived" }),
		).not.toBeInTheDocument();
	},
};

export const EscapeClosesPopover: Story = {
	play: async ({ canvasElement }) => {
		const canvas = within(canvasElement);
		const body = within(document.body);

		await userEvent.click(
			canvas.getByRole("button", { name: "Filter agents" }),
		);
		await expect(
			await body.findByRole("dialog", { name: "Filter agents" }),
		).toBeInTheDocument();
		await userEvent.keyboard("{Escape}");

		await waitFor(() => {
			expect(
				body.queryByRole("dialog", { name: "Filter agents" }),
			).not.toBeInTheDocument();
		});
	},
};
