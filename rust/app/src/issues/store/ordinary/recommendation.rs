use serde::{Deserialize, Serialize};
use serde_json::json;

#[derive(Deserialize)]
pub(super) struct CatalogSeed {
    builtins: Vec<Template>,
    legacy_agile: Vec<Template>,
}

#[derive(Clone, Deserialize, Serialize, Default)]
#[serde(default)]
struct Template {
    id: String,
    revision: i64,
    name: String,
    kind: String,
    keywords: Option<Vec<String>>,
    steps: Option<Vec<Step>>,
    review: String,
    rework_step: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    github_delivery: Option<GitHubDelivery>,
}

#[derive(Clone, Deserialize, Serialize, Default)]
#[serde(default)]
struct GitHubDelivery {
    repository: String,
    base_ref: String,
}

#[derive(Clone, Deserialize, Serialize, Default, PartialEq, Eq)]
#[serde(default)]
struct Actor {
    kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    id: String,
}

#[derive(Clone, Deserialize, Serialize, Default, PartialEq, Eq)]
#[serde(default)]
struct Step {
    #[serde(skip_serializing_if = "is_false")]
    require_preflight: bool,
    #[serde(skip_serializing_if = "is_false")]
    self_claim: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    acceptance_gate: String,
    name: String,
    output: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    instructions: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    inputs: String,
    #[serde(skip_serializing_if = "no_checks")]
    checks: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    owner: Option<Actor>,
    #[serde(skip_serializing_if = "Option::is_none")]
    reviewer: Option<Actor>,
}

fn is_false(value: &bool) -> bool {
    !value
}

fn no_checks(checks: &Option<Vec<String>>) -> bool {
    checks.as_ref().is_none_or(Vec::is_empty)
}

impl CatalogSeed {
    pub(super) fn recommend(
        self,
        stored: &str,
        title: &str,
        description: &str,
        goal: &str,
    ) -> Result<Option<String>, serde_json::Error> {
        let custom = serde_json::from_str::<Option<Vec<Template>>>(stored)?.unwrap_or_default();
        let mut catalog = self.builtins;
        let current_agile = catalog.iter().find(|item| item.id == "agile").cloned();
        for mut method in custom {
            if method.revision == 0 {
                method.revision = 1;
            }
            if method.id == "agile"
                && let Some(current) = &current_agile
            {
                upgrade_agile(&mut method, current, &self.legacy_agile);
            }
            if let Some(previous) = catalog.iter_mut().find(|item| item.id == method.id) {
                *previous = method;
            } else {
                catalog.push(method);
            }
        }

        let delivery = task_outcome(description);
        for (source, label, text) in [
            ("title", "事项标题", title),
            ("deliverable", "事项交付物", delivery.as_str()),
            ("goal", "关联目标", goal),
        ] {
            let matches = best_matches(&catalog, text);
            match matches.as_slice() {
                [] => continue,
                [(template, word)] => {
                    let mut template = (*template).clone();
                    if template.revision == 0 {
                        template.revision = 1;
                    }
                    template.keywords.get_or_insert_default();
                    let reason = format!("{label}匹配“{word}”，选用「{}」。", template.name);
                    let run = json!({
                        "source": source,
                        "revision": 0,
                        "state": "ready",
                        "method": template,
                        "step": 0,
                        "reason": reason,
                        "history": [],
                    });
                    return Ok(Some(run.to_string()));
                }
                // Go asks the user to choose on an ambiguity and persists no run.
                _ => return Ok(None),
            }
        }
        Ok(None)
    }
}

fn copy_step_assignment(to: &mut Step, from: &Step) {
    to.owner.clone_from(&from.owner);
    to.reviewer.clone_from(&from.reviewer);
    to.self_claim = from.self_claim;
}

fn upgrade_agile(method: &mut Template, current: &Template, legacy: &[Template]) {
    let Some(steps) = &method.steps else { return };
    for old in legacy {
        if method.kind != old.kind {
            continue;
        }
        let Some(mut old_steps) = old.steps.clone() else {
            continue;
        };
        if old_steps.len() != steps.len() {
            continue;
        }
        for (old_step, step) in old_steps.iter_mut().zip(steps) {
            copy_step_assignment(old_step, step);
        }
        if old_steps != *steps {
            continue;
        }
        let mut new_steps = current.steps.clone().unwrap_or_default();
        for (new_step, step) in new_steps.iter_mut().zip(steps) {
            copy_step_assignment(new_step, step);
        }
        method.steps = Some(new_steps);
        if method.review == old.review {
            method.review.clone_from(&current.review);
        }
        method.revision += current.revision - old.revision;
        return;
    }
}

// Go's strings.ToLower is a per-rune simple mapping. In particular a capital
// dotted I maps to i without a combining dot and sigma ignores its context.
fn lower(text: &str) -> String {
    text.chars()
        .map(|c| c.to_lowercase().next().unwrap_or(c))
        .collect()
}

fn best_matches<'a>(catalog: &'a [Template], text: &str) -> Vec<(&'a Template, &'a str)> {
    let text = lower(text.trim());
    let mut matches = Vec::new();
    let mut high = 0;
    for method in catalog {
        let mut score = 0;
        let mut strongest = "";
        for word in method.keywords.iter().flatten() {
            let candidate = word_score(&text, word);
            if candidate > score
                || (candidate > 0 && candidate == score && word.len() > strongest.len())
            {
                score = candidate;
                strongest = word;
            }
        }
        if score == 0 || score < high {
            continue;
        }
        if score > high {
            high = score;
            matches.clear();
        }
        matches.push((method, strongest));
    }
    matches
}

fn word_score(text: &str, word: &str) -> u8 {
    let word = lower(word.trim());
    let bytes = word.as_bytes();
    if bytes.is_empty() {
        return 0;
    }
    let ascii_word = |b: u8| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_';
    for (start, _) in text.match_indices(&word) {
        let end = start + bytes.len();
        if start > 0 && ascii_word(bytes[0]) && ascii_word(text.as_bytes()[start - 1]) {
            continue;
        }
        if end < text.len()
            && ascii_word(bytes[bytes.len() - 1])
            && ascii_word(text.as_bytes()[end])
        {
            continue;
        }
        let tail = text[end..].trim();
        if start == 0
            && !["与", "和", "及", "、", "and "]
                .iter()
                .any(|prefix| tail.starts_with(prefix))
        {
            return 2;
        }
        return 1;
    }
    0
}

fn task_outcome(body: &str) -> String {
    let lines: Vec<_> = body.split('\n').collect();
    let mut outcomes = Vec::new();
    for (index, line) in lines.iter().enumerate() {
        let line = line.trim().trim_start_matches(['#', '*', '-', ' ']).trim();
        let normalized = line.replace('：', ":");
        let (label, value) = normalized.split_once(':').unwrap_or((&normalized, ""));
        let lower_line = lower(line);
        if matches!(
            lower(label.trim()).as_str(),
            "交付物"
                | "预期产出"
                | "预期结果"
                | "任务目标"
                | "交付目标"
                | "交付结果"
                | "成果"
                | "deliverable"
                | "expected outcome"
                | "expected result"
                | "objective"
        ) {
            let value = if value.trim().is_empty() {
                lines.get(index + 1).copied().unwrap_or(value)
            } else {
                value
            };
            outcomes.push(value.trim().to_owned());
        } else if line.starts_with("作为") || lower_line.starts_with("as a ") {
            outcomes.push(line.to_owned());
        } else if index == 0
            && let Some(prefix) = ["请", "需要", "希望", "please "]
                .iter()
                .find(|prefix| lower_line.starts_with(**prefix))
        {
            outcomes.push(line[prefix.len()..].trim().to_owned());
        }
    }
    outcomes.join("\n")
}

pub(super) fn is_ready(raw: &str) -> Result<bool, serde_json::Error> {
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Stage {
        state: String,
    }
    Ok(serde_json::from_str::<Option<Stage>>(raw)?.is_some_and(|run| run.state == "ready"))
}
