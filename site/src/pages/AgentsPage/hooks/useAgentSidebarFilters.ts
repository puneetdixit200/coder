import { useSearchParams } from "react-router";
import type { ChatListPRStatusFilter } from "#/api/queries/chats";

export type ArchivedFilter = "active" | "archived";
export type AgentSidebarGroupBy = "date" | "chat_status";
export const AGENT_PR_STATUS_ORDER = [
	"draft",
	"open",
	"merged",
	"closed",
] as const satisfies readonly ChatListPRStatusFilter[];

export type AgentPRStatusFilter = ChatListPRStatusFilter;

export type AgentSidebarFilters = Readonly<{
	archived: ArchivedFilter;
	groupBy: AgentSidebarGroupBy;
	prStatuses: readonly AgentPRStatusFilter[];
	unreadOnly: boolean;
}>;

type UseAgentSidebarFiltersResult = readonly [
	filters: AgentSidebarFilters,
	setFilters: (next: AgentSidebarFilters) => void,
	clearFilters: () => void,
];

const DEFAULT_FILTERS: AgentSidebarFilters = {
	archived: "active",
	groupBy: "date",
	prStatuses: [],
	unreadOnly: false,
};

const agentPRStatusSet = new Set<AgentPRStatusFilter>(AGENT_PR_STATUS_ORDER);

const isAgentPRStatusFilter = (value: string): value is AgentPRStatusFilter => {
	return agentPRStatusSet.has(value as AgentPRStatusFilter);
};

const canonicalizePRStatuses = (
	values: Iterable<string>,
): readonly AgentPRStatusFilter[] => {
	const selected = new Set<AgentPRStatusFilter>();
	for (const value of values) {
		if (isAgentPRStatusFilter(value)) {
			selected.add(value);
		}
	}
	return AGENT_PR_STATUS_ORDER.filter((status) => selected.has(status));
};

const parsePRStatuses = (
	searchParams: URLSearchParams,
): readonly AgentPRStatusFilter[] => {
	return canonicalizePRStatuses(
		searchParams
			.getAll("pr_status")
			.flatMap((value) => value.split(","))
			.map((value) => value.trim().toLowerCase())
			.filter(Boolean),
	);
};

const clearSidebarFilterParams = (searchParams: URLSearchParams) => {
	searchParams.delete("archived");
	searchParams.delete("group_by");
	searchParams.delete("pr_status");
	searchParams.delete("chat_status");
};

const writeSidebarFilters = (
	searchParams: URLSearchParams,
	filters: AgentSidebarFilters,
) => {
	clearSidebarFilterParams(searchParams);

	if (filters.archived === "archived") {
		searchParams.set("archived", "archived");
	}
	if (filters.groupBy === "chat_status") {
		searchParams.set("group_by", "chat_status");
	}

	const prStatuses = canonicalizePRStatuses(filters.prStatuses);
	if (prStatuses.length > 0) {
		searchParams.set("pr_status", prStatuses.join(","));
	}
	if (filters.unreadOnly) {
		searchParams.set("chat_status", "unread");
	}
};

export const useAgentSidebarFilters = (): UseAgentSidebarFiltersResult => {
	const [searchParams, setSearchParams] = useSearchParams();

	const filters: AgentSidebarFilters = {
		archived:
			searchParams.get("archived") === "archived"
				? "archived"
				: DEFAULT_FILTERS.archived,
		groupBy:
			searchParams.get("group_by") === "chat_status"
				? "chat_status"
				: DEFAULT_FILTERS.groupBy,
		prStatuses: parsePRStatuses(searchParams),
		unreadOnly: searchParams.get("chat_status") === "unread",
	};

	const setFilters = (next: AgentSidebarFilters) => {
		setSearchParams(
			(prev) => {
				const updated = new URLSearchParams(prev);
				writeSidebarFilters(updated, next);
				return updated;
			},
			{ replace: true },
		);
	};

	const clearFilters = () => {
		setSearchParams(
			(prev) => {
				const updated = new URLSearchParams(prev);
				clearSidebarFilterParams(updated);
				return updated;
			},
			{ replace: true },
		);
	};

	return [filters, setFilters, clearFilters];
};
