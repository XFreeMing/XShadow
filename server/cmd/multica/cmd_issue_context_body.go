package main

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// Story bodies are free text written by people and agents: the same fields
// arrive as ATX headings, bold lines, "label:" lines, or inline bullets, in
// Chinese or English, and future stories may use a shape this file has never
// seen. The parser therefore never invents meaning. It recognises a section
// whose heading is one of the agreed names, extracts the list items written
// under it, and records the exact source lines. Anything it cannot find is
// reported as insufficient rather than replaced by the title or a summary.

const (
	storySectionOther        = "other"
	storySectionAcceptance   = "acceptance"
	storySectionResult       = "result"
	storySectionContribution = "contribution"
)

var (
	// atxHeadingRe matches a Markdown ATX heading (# through ######).
	atxHeadingRe = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*#*[ \t]*$`)
	// boldHeadingRe matches a line that is only a bold phrase, optionally
	// followed by a colon: "**验收标准**", "**Acceptance criteria:**".
	boldHeadingRe = regexp.MustCompile(`^ {0,3}(?:\*\*|__)(.+?)(?:\*\*|__)[ \t]*[:：]?[ \t]*$`)
	// labelRe matches a "label: content" line. Prose with a colon in the
	// middle can match too, so the label is only treated as a section
	// boundary when it names a known field.
	labelRe = regexp.MustCompile(`^ {0,3}(?:\*\*|__)?([^[:space:]:：*_#\-+|>][^:：*_#]{0,48}?)(?:\*\*|__)?[ \t]*[:：][ \t]*(.*)$`)
	// bulletLabelRe matches a known label written as a list item, e.g.
	// "- 可观察验收：用户能…". Issue templates use this inline form.
	bulletLabelRe = regexp.MustCompile(`^ {0,3}(?:[-*+]|\d+[.)、])[ \t]+(?:\*\*|__)?([^[:space:]:：*_#]{1,48}?)(?:\*\*|__)?[ \t]*[:：][ \t]*(.*)$`)
	// bulletRe matches a bulleted or ordered list item.
	bulletRe = regexp.MustCompile(`^ {0,3}(?:[-*+]|\d+[.)、])[ \t]+(.*)$`)
	// checkboxRe matches a task-list item and captures its check state.
	checkboxRe = regexp.MustCompile(`^ {0,3}(?:[-*+]|\d+[.)、])[ \t]+\[([ xX])\][ \t]+(.*)$`)
)

var acceptanceSectionNames = map[string]struct{}{
	"验收": {}, "验收标准": {}, "验收条件": {}, "验收要点": {}, "验收清单": {},
	"完成标准": {}, "完成定义": {}, "完成条件": {}, "检查标准": {}, "检查清单": {},
	"可观察验收": {}, "可观察的验收": {}, "范围与可观察验收": {}, "范围与验收": {},
	"本故事验收条件": {}, "本故事验收标准": {}, "手动验收": {}, "验收结果": {},
	"acceptance": {}, "acceptance criteria": {}, "acceptance criterion": {},
	"acceptance test": {}, "acceptance tests": {}, "acceptance checklist": {},
	"definition of done": {}, "definition of ready": {}, "dod": {}, "criteria": {}, "checklist": {},
}

var resultSectionNames = map[string]struct{}{
	"结果": {}, "用户结果": {}, "预期结果": {}, "本次结果": {}, "用户价值": {},
	"交付": {}, "交付物": {}, "交付结果": {}, "可交付结果": {}, "交付内容": {}, "产物": {},
	"用户得到的结果": {}, "结果与影响": {}, "用户故事": {},
	"result": {}, "results": {}, "outcome": {}, "outcomes": {},
	"deliverable": {}, "deliverables": {}, "delivery": {},
	"user value": {}, "value": {}, "user story": {},
}

var contributionSectionNames = map[string]struct{}{
	"贡献": {}, "对目标的贡献": {}, "本故事的贡献": {}, "本故事贡献": {},
	"目标贡献": {}, "价值贡献": {}, "对上层目标的贡献": {},
	"contribution": {}, "goal contribution": {}, "value contribution": {},
}

type storyLine struct {
	Number int
	Text   string
	Code   bool
}

type storySection struct {
	Kind      string
	Heading   string
	LineStart int
	LineEnd   int
	Lines     []storyLine
}

// storyContext is the parsed story layer. Values are the output types so the
// command does not need a second copy of every field.
type storyContext struct {
	Body                 issueContextBodyLayer
	Result               issueContextValue
	Contribution         issueContextValue
	Statement            *issueContextValue
	Acceptance           issueContextAcceptance
	ExplicitContribution bool
}

// normalizeStoryHeading strips Markdown marks, a trailing colon, case and
// repeated whitespace so "## 验收条件：" and "**验收条件**" compare equally.
func normalizeStoryHeading(text string) string {
	trimmed := strings.TrimSpace(text)
	trimmed = strings.Trim(trimmed, "*_`# ")
	trimmed = strings.TrimSpace(trimmed)
	trimmed = strings.TrimRight(trimmed, ":：")
	trimmed = strings.ToLower(strings.Join(strings.Fields(trimmed), " "))
	return trimmed
}

// classifyStorySection maps a heading to the layer it documents. Acceptance
// is checked first: a heading such as "验收结果" is acceptance, not result.
// "范围与可观察验收" is the heading the current story template uses; it is a
// scope line plus acceptance in one section, so its list items are read as
// acceptance and its lead paragraph stays context.
func classifyStorySection(heading string) string {
	normalized := normalizeStoryHeading(heading)
	if normalized == "" {
		return storySectionOther
	}
	if _, ok := acceptanceSectionNames[normalized]; ok {
		return storySectionAcceptance
	}
	if _, ok := contributionSectionNames[normalized]; ok {
		return storySectionContribution
	}
	if strings.Contains(normalized, "贡献") || strings.Contains(normalized, "contribution") {
		return storySectionContribution
	}
	if _, ok := resultSectionNames[normalized]; ok {
		return storySectionResult
	}
	if strings.Contains(normalized, "交付物") || strings.Contains(normalized, "产物") {
		return storySectionResult
	}
	if strings.HasSuffix(normalized, "结果") {
		return storySectionResult
	}
	return storySectionOther
}

// classifyStoryLabel maps an inline "label：content" boundary. Prose often
// ends a lead-in with a colon ("本故事完成后，用户能看到以下结果："), so only
// the agreed field names may start a section on an inline label; the broader
// heuristics above apply to headings, which are short by construction.
func classifyStoryLabel(label string) string {
	normalized := normalizeStoryHeading(label)
	if normalized == "" {
		return storySectionOther
	}
	if _, ok := acceptanceSectionNames[normalized]; ok {
		return storySectionAcceptance
	}
	if _, ok := contributionSectionNames[normalized]; ok {
		return storySectionContribution
	}
	if _, ok := resultSectionNames[normalized]; ok {
		return storySectionResult
	}
	return storySectionOther
}

// splitHeading decides whether a line starts a section. A known label is a
// boundary even when written inline ("验收标准：用户能…") or as a list item
// ("- 可观察验收：…"), because both forms appear in hand-written stories.
func splitHeading(line string) (heading, inline string, ok bool) {
	if m := atxHeadingRe.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(m[2]), "", true
	}
	if m := boldHeadingRe.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(m[1]), "", true
	}
	if m := bulletLabelRe.FindStringSubmatch(line); m != nil {
		label := strings.TrimSpace(m[1])
		if classifyStoryLabel(label) != storySectionOther {
			return label, strings.TrimSpace(m[2]), true
		}
	}
	if m := labelRe.FindStringSubmatch(line); m != nil {
		label := strings.TrimSpace(m[1])
		if classifyStoryLabel(label) != storySectionOther {
			return label, strings.TrimSpace(m[2]), true
		}
	}
	return "", "", false
}

func isFence(line string) (marker string, ok bool) {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return "```", true
	case strings.HasPrefix(trimmed, "~~~"):
		return "~~~", true
	}
	return "", false
}

// parseStorySections splits a description into headed sections. A section
// runs from its heading to the line before the next heading; content inside
// a fenced code block never starts a section and is never read as an item.
func parseStorySections(description string) []storySection {
	normalized := strings.ReplaceAll(description, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")

	var sections []storySection
	var current *storySection
	var fence string
	for index, line := range lines {
		number := index + 1
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			if current != nil {
				current.Lines = append(current.Lines, storyLine{Number: number, Text: line, Code: true})
			}
			continue
		}
		if marker, ok := isFence(line); ok {
			fence = marker
			if current != nil {
				current.Lines = append(current.Lines, storyLine{Number: number, Text: line, Code: true})
			}
			continue
		}
		if heading, inline, ok := splitHeading(line); ok {
			if current != nil {
				current.LineEnd = number - 1
				sections = append(sections, *current)
			}
			current = &storySection{
				Kind:      classifyStorySection(heading),
				Heading:   heading,
				LineStart: number,
			}
			if inline != "" {
				current.Lines = append(current.Lines, storyLine{Number: number, Text: inline})
			}
			continue
		}
		if current != nil {
			current.Lines = append(current.Lines, storyLine{Number: number, Text: line})
		}
	}
	if current != nil {
		current.LineEnd = len(lines)
		sections = append(sections, *current)
	}
	return sections
}

func trimStoryLines(lines []storyLine) []storyLine {
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start].Text) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1].Text) == "" {
		end--
	}
	return lines[start:end]
}

// sectionText returns a section's content verbatim (trimmed of surrounding
// blank lines) so an extracted result or contribution can be checked against
// the source.
func sectionText(section storySection) string {
	lines := trimStoryLines(section.Lines)
	if len(lines) == 0 {
		return ""
	}
	parts := make([]string, len(lines))
	for i, line := range lines {
		parts[i] = line.Text
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// sectionItems extracts the acceptance list. Bullets, numbered items and
// task-list checkboxes are read individually; when a section has no list at
// all, its paragraphs are read as items so hand-written one-liners still
// count. A lead paragraph before a list is scope text, not an item, which is
// how "范围与可观察验收" is written: one context sentence, then numbered
// observable results.
func sectionItems(section storySection) []issueContextAcceptanceItem {
	lines := trimStoryLines(section.Lines)
	if len(lines) == 0 {
		return nil
	}

	hasList := false
	for _, line := range lines {
		if line.Code || strings.TrimSpace(line.Text) == "" {
			continue
		}
		if checkboxRe.MatchString(line.Text) || bulletRe.MatchString(line.Text) {
			hasList = true
			break
		}
	}

	if hasList {
		items := make([]issueContextAcceptanceItem, 0, len(lines))
		for _, line := range lines {
			if line.Code {
				continue
			}
			trimmed := strings.TrimSpace(line.Text)
			if trimmed == "" {
				continue
			}
			if m := checkboxRe.FindStringSubmatch(line.Text); m != nil {
				checked := strings.EqualFold(m[1], "x")
				items = append(items, issueContextAcceptanceItem{Text: strings.TrimSpace(m[2]), Checked: &checked, Line: line.Number})
				continue
			}
			if m := bulletRe.FindStringSubmatch(line.Text); m != nil {
				items = append(items, issueContextAcceptanceItem{Text: strings.TrimSpace(m[1]), Line: line.Number})
				continue
			}
			// An indented continuation belongs to the item above; any other
			// text is the section's intro or a note, not an acceptance item.
			if len(items) > 0 && (strings.HasPrefix(line.Text, " ") || strings.HasPrefix(line.Text, "\t")) {
				last := &items[len(items)-1]
				last.Text = strings.TrimSpace(last.Text + " " + trimmed)
			}
		}
		return items
	}

	var items []issueContextAcceptanceItem
	var paragraph []storyLine
	flush := func() {
		if len(paragraph) == 0 {
			return
		}
		texts := make([]string, 0, len(paragraph))
		for _, line := range paragraph {
			texts = append(texts, strings.TrimSpace(line.Text))
		}
		if text := strings.TrimSpace(strings.Join(texts, "\n")); text != "" {
			items = append(items, issueContextAcceptanceItem{Text: text, Line: paragraph[0].Number})
		}
		paragraph = nil
	}
	for _, line := range lines {
		if line.Code {
			continue
		}
		if strings.TrimSpace(line.Text) == "" {
			flush()
			continue
		}
		paragraph = append(paragraph, line)
	}
	flush()
	return items
}

// storyStatement finds the description's first real paragraph and returns it
// only when it is written in user-story form ("作为…我希望…以便…" or "As a…
// I want… so that…"). That sentence belongs to the story's own result; a
// caller may show it as the contribution "shown by the story result" without
// inventing an upper-goal contribution.
func storyStatement(description string) *issueContextValue {
	normalized := strings.ReplaceAll(description, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")

	var paragraph []storyLine
	flush := func() *issueContextValue {
		if len(paragraph) == 0 {
			return nil
		}
		texts := make([]string, 0, len(paragraph))
		for _, line := range paragraph {
			texts = append(texts, strings.TrimSpace(line.Text))
		}
		text := strings.TrimSpace(strings.Join(texts, " "))
		if !looksLikeUserStory(text) {
			return nil
		}
		return &issueContextValue{
			State:     "extracted",
			Text:      text,
			Basis:     "story_statement",
			LineStart: paragraph[0].Number,
			LineEnd:   paragraph[len(paragraph)-1].Number,
			Note:      "the story's user-story statement; its contribution is shown by the story result it promises, not restated as an upper-goal claim",
		}
	}

	for index, line := range lines {
		number := index + 1
		trimmed := strings.TrimSpace(line)
		if _, _, isHeading := splitHeading(line); isHeading {
			if value := flush(); value != nil {
				return value
			}
			paragraph = nil
			continue
		}
		if trimmed == "" {
			if value := flush(); value != nil {
				return value
			}
			continue
		}
		paragraph = append(paragraph, storyLine{Number: number, Text: line})
	}
	return flush()
}

func looksLikeUserStory(text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(text, "作为") {
		wants := strings.Contains(text, "我希望") || strings.Contains(text, "我想要") ||
			strings.Contains(text, "希望获得") || strings.Contains(text, "希望得到")
		benefit := strings.Contains(text, "以便") || strings.Contains(text, "从而") || strings.Contains(text, "解决")
		return wants && benefit
	}
	englishAs := strings.Contains(lower, "as a ") || strings.Contains(lower, "as an ")
	englishWant := strings.Contains(lower, "i want") || strings.Contains(lower, "i would like") || strings.Contains(lower, "i hope")
	englishBenefit := strings.Contains(lower, "so that") || strings.Contains(lower, "in order to")
	return englishAs && englishWant && englishBenefit
}

// buildStoryContext parses the issue description. The full body is always
// kept as the evidence of record; the extracted values only ever come from it.
func buildStoryContext(description string, revision int64) storyContext {
	context := storyContext{
		Body: issueContextBodyLayer{
			State:     "present",
			Text:      description,
			SHA256:    sha256Hex(description),
			LineCount: descriptionLineCount(description),
		},
		Acceptance: issueContextAcceptance{
			State:    "insufficient",
			Items:    []issueContextAcceptanceItem{},
			Sections: []issueContextAcceptanceSection{},
			Source:   contextDescriptionSource(revision),
		},
	}
	if strings.TrimSpace(description) == "" {
		context.Body = issueContextBodyLayer{State: "empty", LineCount: 0}
		context.Acceptance.Reason = "the issue description is empty"
		context.Result = issueContextValue{State: "unavailable", Basis: "none", Note: "the issue description is empty"}
		context.Contribution = issueContextValue{State: "unavailable", Basis: "none", Note: "the issue description is empty"}
		return context
	}

	var resultSections, contributionSections, acceptanceSections []storySection
	for _, section := range parseStorySections(description) {
		switch section.Kind {
		case storySectionResult:
			if sectionText(section) != "" {
				resultSections = append(resultSections, section)
			}
		case storySectionContribution:
			if sectionText(section) != "" {
				contributionSections = append(contributionSections, section)
			}
		case storySectionAcceptance:
			acceptanceSections = append(acceptanceSections, section)
		}
	}

	if len(resultSections) > 0 {
		first := resultSections[0]
		context.Result = issueContextValue{
			State:     "extracted",
			Text:      sectionText(first),
			Basis:     "section",
			Section:   first.Heading,
			LineStart: first.LineStart,
			LineEnd:   first.LineEnd,
			Source:    "issue description, revision " + formatContextRevision(revision),
		}
	} else {
		context.Result = issueContextValue{
			State: "basis_only",
			Basis: "full_description",
			Note:  "no dedicated result section; the full description and recent feedback are the evidence",
		}
	}

	if len(contributionSections) > 0 {
		first := contributionSections[0]
		context.ExplicitContribution = true
		context.Contribution = issueContextValue{
			State:     "extracted",
			Text:      sectionText(first),
			Basis:     "section",
			Section:   first.Heading,
			LineStart: first.LineStart,
			LineEnd:   first.LineEnd,
			Source:    "issue description, revision " + formatContextRevision(revision),
		}
	} else {
		context.Contribution = issueContextValue{
			State: "basis_only",
			Basis: "full_description",
			Note:  "no dedicated contribution section; the full description and recent feedback are the evidence, and no contribution is inferred",
		}
	}
	context.Statement = storyStatement(description)
	if context.Statement != nil {
		context.Statement.Source = contextDescriptionSource(revision)
	}

	seen := make(map[string]struct{})
	for _, section := range acceptanceSections {
		context.Acceptance.Sections = append(context.Acceptance.Sections, issueContextAcceptanceSection{
			Heading:   section.Heading,
			LineStart: section.LineStart,
			LineEnd:   section.LineEnd,
		})
		for _, item := range sectionItems(section) {
			key := strings.TrimSpace(item.Text)
			if key == "" {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			context.Acceptance.Items = append(context.Acceptance.Items, item)
		}
	}
	if len(context.Acceptance.Items) > 0 {
		context.Acceptance.State = "found"
	} else if len(acceptanceSections) > 0 {
		context.Acceptance.Reason = "an acceptance section exists but contains no extractable item"
	} else {
		context.Acceptance.Reason = "no dedicated acceptance section was found in the description"
	}
	return context
}

func contextDescriptionSource(revision int64) string {
	return "issue description, revision " + formatContextRevision(revision)
}

func formatContextRevision(revision int64) string {
	return strconv.FormatInt(revision, 10)
}

func descriptionLineCount(description string) int {
	if description == "" {
		return 0
	}
	normalized := strings.ReplaceAll(description, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Count(normalized, "\n") + 1
}

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
