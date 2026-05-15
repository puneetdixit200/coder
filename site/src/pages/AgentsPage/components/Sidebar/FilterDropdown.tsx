import { FilterIcon } from "lucide-react";
import { type FC, useId, useState } from "react";
import { Button } from "#/components/Button/Button";
import { Checkbox } from "#/components/Checkbox/Checkbox";
import {
	Popover,
	PopoverContent,
	PopoverTrigger,
} from "#/components/Popover/Popover";
import { RadioGroup, RadioGroupItem } from "#/components/RadioGroup/RadioGroup";
import { SearchField } from "#/components/SearchField/SearchField";
import { cn } from "#/utils/cn";
import {
	AGENT_PR_STATUS_ORDER,
	type AgentPRStatusFilter,
	type AgentSidebarFilters,
	type AgentSidebarGroupBy,
	type ArchivedFilter,
} from "../../hooks/useAgentSidebarFilters";

const DEFAULT_FILTERS: AgentSidebarFilters = {
	archived: "active",
	groupBy: "date",
	prStatuses: [],
	unreadOnly: false,
};

const PR_STATUS_LABELS: Record<AgentPRStatusFilter, string> = {
	draft: "Draft",
	open: "Open",
	merged: "Merged",
	closed: "Closed",
};

interface FilterDropdownProps {
	readonly filters: AgentSidebarFilters;
	readonly onFiltersChange: (filters: AgentSidebarFilters) => void;
}

const hasActiveFilters = (filters: AgentSidebarFilters): boolean => {
	return (
		filters.archived !== DEFAULT_FILTERS.archived ||
		filters.groupBy !== DEFAULT_FILTERS.groupBy ||
		filters.prStatuses.length > 0 ||
		filters.unreadOnly
	);
};

export const FilterDropdown: FC<FilterDropdownProps> = ({
	filters,
	onFiltersChange,
}) => {
	const id = useId();
	const [open, setOpen] = useState(false);
	const [stagedFilters, setStagedFilters] =
		useState<AgentSidebarFilters>(filters);
	const [optionSearch, setOptionSearch] = useState("");

	const handleOpenChange = (nextOpen: boolean) => {
		if (nextOpen) {
			setStagedFilters(filters);
			setOptionSearch("");
		}
		setOpen(nextOpen);
	};

	const normalizedOptionSearch = optionSearch.trim().toLowerCase();
	const matchesOption = (...labels: readonly string[]) => {
		return (
			normalizedOptionSearch === "" ||
			labels.some((label) =>
				label.toLowerCase().includes(normalizedOptionSearch),
			)
		);
	};

	const archiveOptions: readonly Readonly<{
		value: ArchivedFilter;
		label: string;
	}>[] = [
		{ value: "active", label: "Active" },
		{ value: "archived", label: "Archived" },
	];
	const visibleArchiveOptions = archiveOptions.filter((option) =>
		matchesOption("Archive status", option.label),
	);
	const visiblePRStatuses = AGENT_PR_STATUS_ORDER.filter((status) =>
		matchesOption("PR status", PR_STATUS_LABELS[status]),
	);
	const showUnreadOption = matchesOption("Chat status", "Unread");
	const showFilterOptions =
		visibleArchiveOptions.length > 0 ||
		visiblePRStatuses.length > 0 ||
		showUnreadOption;

	const setGroupBy = (value: string) => {
		if (value !== "date" && value !== "chat_status") {
			return;
		}
		const groupBy: AgentSidebarGroupBy = value;
		setStagedFilters({ ...stagedFilters, groupBy });
	};

	const setArchived = (value: string) => {
		if (value !== "active" && value !== "archived") {
			return;
		}
		const archived: ArchivedFilter = value;
		setStagedFilters({ ...stagedFilters, archived });
	};

	const setPRStatus = (status: AgentPRStatusFilter, checked: boolean) => {
		const selected = new Set(stagedFilters.prStatuses);
		if (checked) {
			selected.add(status);
		} else {
			selected.delete(status);
		}
		setStagedFilters({
			...stagedFilters,
			prStatuses: AGENT_PR_STATUS_ORDER.filter((value) => selected.has(value)),
		});
	};

	const applyFilters = () => {
		onFiltersChange(stagedFilters);
		setOpen(false);
	};

	const clearFilters = () => {
		setStagedFilters(DEFAULT_FILTERS);
		setOptionSearch("");
	};

	return (
		<Popover open={open} onOpenChange={handleOpenChange}>
			<PopoverTrigger asChild>
				<Button
					variant="subtle"
					size="icon"
					aria-label="Filter agents"
					className={cn(
						"h-7 w-7 min-w-0 justify-end rounded-none px-0 text-content-secondary hover:text-content-primary",
						hasActiveFilters(filters) && "text-content-primary",
					)}
				>
					<FilterIcon />
				</Button>
			</PopoverTrigger>
			<PopoverContent
				align="end"
				aria-label="Filter agents"
				role="dialog"
				className="mobile-full-width-dropdown mobile-full-width-dropdown-top-below-header w-80 p-0 text-[13px]"
			>
				<div className="flex flex-col gap-4 p-4">
					<section className="space-y-2">
						<h2
							id={`${id}-group-heading`}
							className="m-0 text-xs font-medium text-content-secondary"
						>
							Group
						</h2>
						<RadioGroup
							aria-labelledby={`${id}-group-heading`}
							value={stagedFilters.groupBy}
							onValueChange={setGroupBy}
							className="gap-1"
						>
							<div className="flex items-center gap-2">
								<RadioGroupItem id={`${id}-group-date`} value="date" />
								<label htmlFor={`${id}-group-date`}>Date</label>
							</div>
							<div className="flex items-center gap-2">
								<RadioGroupItem
									id={`${id}-group-chat-status`}
									value="chat_status"
								/>
								<label htmlFor={`${id}-group-chat-status`}>Chat status</label>
							</div>
						</RadioGroup>
					</section>

					<section className="space-y-3">
						<h2 className="m-0 text-xs font-medium text-content-secondary">
							Filter by
						</h2>
						<SearchField
							value={optionSearch}
							onChange={setOptionSearch}
							placeholder="Search filters..."
							aria-label="Search filters"
							className="w-full"
						/>

						{visibleArchiveOptions.length > 0 && (
							<div className="space-y-2">
								<h3
									id={`${id}-archive-heading`}
									className="m-0 text-xs font-medium text-content-secondary"
								>
									Archive status
								</h3>
								<RadioGroup
									aria-labelledby={`${id}-archive-heading`}
									value={stagedFilters.archived}
									onValueChange={setArchived}
									className="gap-1"
								>
									{visibleArchiveOptions.map((option) => (
										<div key={option.value} className="flex items-center gap-2">
											<RadioGroupItem
												id={`${id}-archive-${option.value}`}
												value={option.value}
											/>
											<label htmlFor={`${id}-archive-${option.value}`}>
												{option.label}
											</label>
										</div>
									))}
								</RadioGroup>
							</div>
						)}

						{visiblePRStatuses.length > 0 && (
							<div className="space-y-2">
								<h3 className="m-0 text-xs font-medium text-content-secondary">
									PR status
								</h3>
								<div className="space-y-1">
									{visiblePRStatuses.map((status) => {
										const checked = stagedFilters.prStatuses.includes(status);
										const checkboxId = `${id}-pr-${status}`;
										return (
											<div key={status} className="flex items-center gap-2">
												<Checkbox
													id={checkboxId}
													checked={checked}
													onCheckedChange={(nextChecked) =>
														setPRStatus(status, nextChecked === true)
													}
												/>
												<label htmlFor={checkboxId}>
													{PR_STATUS_LABELS[status]}
												</label>
											</div>
										);
									})}
								</div>
							</div>
						)}

						{showUnreadOption && (
							<div className="space-y-2">
								<h3 className="m-0 text-xs font-medium text-content-secondary">
									Chat status
								</h3>
								<div className="flex items-center gap-2">
									<Checkbox
										id={`${id}-chat-unread`}
										checked={stagedFilters.unreadOnly}
										onCheckedChange={(checked) =>
											setStagedFilters({
												...stagedFilters,
												unreadOnly: checked === true,
											})
										}
									/>
									<label htmlFor={`${id}-chat-unread`}>Unread</label>
								</div>
							</div>
						)}

						{!showFilterOptions && (
							<p className="m-0 text-content-secondary">No filters found</p>
						)}
					</section>
				</div>
				<div className="flex items-center justify-between border-0 border-t border-solid border-border-default p-3">
					<Button variant="subtle" size="sm" onClick={clearFilters}>
						Clear all
					</Button>
					<Button size="sm" onClick={applyFilters}>
						Apply
					</Button>
				</div>
			</PopoverContent>
		</Popover>
	);
};
