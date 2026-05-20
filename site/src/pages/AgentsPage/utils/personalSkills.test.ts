import { describe, expect, it } from "vitest";
import type * as TypesGen from "#/api/typesGenerated";
import {
	buildPersonalSkillMarkdown,
	filterPersonalSkills,
	getPersonalSkillContentSizeBytes,
	isPersonalSkillTriggerToken,
	isValidPersonalSkillName,
	PERSONAL_SKILL_MAX_SIZE_BYTES,
	parsePersonalSkillMarkdown,
	parsePersonalSkillTrigger,
	personalSkillTriggerText,
	tryParsePersonalSkillMarkdown,
} from "./personalSkills";

const now = "2026-05-08T00:00:00Z";

const skill = (
	name: string,
	description: string,
	index: number,
): TypesGen.UserSkillMetadata => ({
	id: `skill-${index}`,
	name,
	description,
	created_at: now,
	updated_at: now,
});

describe("filterPersonalSkills", () => {
	const skills = [
		skill("deploy", "Ship reviewed production changes", 0),
		skill("reviewer", "Review changed files", 1),
		skill("docs", "Draft deployment docs", 2),
		skill("api-review", "Review API changes", 3),
	];

	it("sorts unfiltered skills by name", () => {
		expect(filterPersonalSkills(skills, "").map(({ name }) => name)).toEqual([
			"api-review",
			"deploy",
			"docs",
			"reviewer",
		]);
	});

	it("ranks prefix, name substring, then description matches", () => {
		expect(filterPersonalSkills(skills, "rev").map(({ name }) => name)).toEqual(
			["reviewer", "api-review", "deploy"],
		);
	});

	it("matches names and descriptions case-insensitively", () => {
		const mixedCaseSkills = [
			skill("deploy-bot", "Ship Changes", 0),
			skill("docs", "Review docs", 1),
		];

		expect(
			filterPersonalSkills(mixedCaseSkills, "DEP").map(({ name }) => name),
		).toEqual(["deploy-bot"]);
		expect(
			filterPersonalSkills(mixedCaseSkills, "changes").map(({ name }) => name),
		).toEqual(["deploy-bot"]);
	});
});

describe("personal skill slash triggers", () => {
	it("formats skill trigger text", () => {
		expect(personalSkillTriggerText(skill("reviewer", "", 0))).toBe(
			"/reviewer",
		);
	});

	it("parses trigger text at line start or after whitespace", () => {
		expect(parsePersonalSkillTrigger("/rev")).toEqual({
			slashOffset: 0,
			query: "rev",
		});
		expect(parsePersonalSkillTrigger("ask /docs")).toEqual({
			slashOffset: 4,
			query: "docs",
		});
	});

	it("rejects mid-token slash triggers", () => {
		expect(parsePersonalSkillTrigger("https://")).toBeNull();
	});

	it("validates replacement trigger tokens", () => {
		expect(isPersonalSkillTriggerToken("/rev")).toBe(true);
		expect(isPersonalSkillTriggerToken("/bad token")).toBe(false);
	});
});

describe("parsePersonalSkillMarkdown", () => {
	it("parses SKILL.md frontmatter and body", () => {
		expect(
			parsePersonalSkillMarkdown(
				'---\nname: test-skill\ndescription: "Does a thing"\n---\n\nUse this skill.',
			),
		).toEqual({
			name: "test-skill",
			description: "Does a thing",
			body: "Use this skill.",
		});
	});

	it("uses backend-compatible parsing for YAML-comment-sensitive values", () => {
		expect(
			parsePersonalSkillMarkdown(
				"---\nname: test-skill\ndescription: Build # test\n---\nBody",
			),
		).toEqual({
			name: "test-skill",
			description: "Build # test",
			body: "Body",
		});
	});
});

describe("tryParsePersonalSkillMarkdown", () => {
	it("returns parsed values for valid SKILL.md content", () => {
		expect(
			tryParsePersonalSkillMarkdown(
				"---\nname: test-skill\ndescription: Does a thing\n---\nBody",
			),
		).toEqual({
			ok: true,
			values: {
				name: "test-skill",
				description: "Does a thing",
				body: "Body",
			},
		});
	});

	it("returns an error message for invalid SKILL.md content", () => {
		expect(
			tryParsePersonalSkillMarkdown(
				"---\ndescription: Missing name\n---\nBody",
			),
		).toEqual({
			ok: false,
			error: "Skill name is required.",
		});
	});
});

describe("buildPersonalSkillMarkdown", () => {
	it("builds backend-compatible skill markdown", () => {
		const content = buildPersonalSkillMarkdown({
			name: "test-skill",
			description: "Does a thing",
			body: "Use this skill.",
		});

		expect(content).toBe(
			"---\nname: test-skill\ndescription: Does a thing\n---\nUse this skill.\n",
		);
		expect(parsePersonalSkillMarkdown(content)).toEqual({
			name: "test-skill",
			description: "Does a thing",
			body: "Use this skill.",
		});
	});
});

describe("isValidPersonalSkillName", () => {
	it("accepts kebab-case skill names", () => {
		expect(isValidPersonalSkillName("test-skill-1")).toBe(true);
	});

	it("rejects names outside the backend pattern", () => {
		expect(isValidPersonalSkillName("Test Skill")).toBe(false);
		expect(isValidPersonalSkillName("test--skill")).toBe(false);
		expect(isValidPersonalSkillName("-test-skill")).toBe(false);
	});
});

describe("getPersonalSkillContentSizeBytes", () => {
	it("counts UTF-8 bytes", () => {
		expect(getPersonalSkillContentSizeBytes("a")).toBe(1);
		expect(getPersonalSkillContentSizeBytes("🧪")).toBe(4);
	});

	it("exposes the backend size limit", () => {
		expect(PERSONAL_SKILL_MAX_SIZE_BYTES).toBe(65_536);
	});
});
