use std::collections::HashSet;

use crate::issues::store::is_uuid;

const MARKER_PREFIX: &str = "<!-- multica:channel-media:";

fn marked_ids(markdown: &str) -> impl Iterator<Item = String> + '_ {
    let mut seen = HashSet::new();
    markdown
        .split(MARKER_PREFIX)
        .skip(1)
        .filter_map(move |tail| {
            let id = tail.get(..36)?;
            if !is_uuid(id) || !tail[36..].starts_with(" -->") {
                return None;
            }
            let id = id.to_ascii_lowercase();
            seen.insert(id.clone()).then_some(id)
        })
}

fn append(markdown: &mut String, block: &str) {
    if !markdown.is_empty() {
        markdown.push_str("\n\n");
    }
    markdown.push_str(block);
}

fn attachment_link(url: &str, filename: &str, content_type: &str) -> String {
    if content_type.starts_with("image/") {
        return format!("![]({url})");
    }
    let mut label = String::new();
    for c in filename.chars() {
        match c {
            '\\' | '[' | ']' => {
                label.push('\\');
                label.push(c);
            }
            '\r' | '\n' => label.push(' '),
            _ => label.push(c),
        }
    }
    if label.is_empty() {
        label.push_str("attachment");
    }
    format!("[{label}]({url})")
}

pub(super) fn merge<'a>(
    current: &str,
    mut incoming: String,
    base: Option<&str>,
    mut attachment: impl FnMut(&str) -> Option<(&'a str, &'a str)>,
) -> String {
    let known = base
        .into_iter()
        .flat_map(marked_ids)
        .collect::<HashSet<_>>();
    for id in marked_ids(current) {
        let Some((filename, content_type)) = attachment(&id) else {
            continue;
        };
        let url = format!("/api/attachments/{id}/download");
        let marker = format!("{MARKER_PREFIX}{id} -->");
        if !incoming.contains(&url) {
            if known.contains(&id) {
                continue;
            }
            append(
                &mut incoming,
                &attachment_link(&url, filename, content_type),
            );
            append(&mut incoming, &marker);
        } else if !incoming.contains(&marker) {
            append(&mut incoming, &marker);
        }
    }
    incoming
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn attachment_markers_preserve_order_deduplicate_and_ignore_missing_files() {
        let a = "a1234567-b123-4567-8999-123456789012";
        let b = "61234567-b123-4567-8999-123456789012";
        let missing = "71234567-b123-4567-8999-123456789012";
        let current = format!(
            "{MARKER_PREFIX}bad --> {MARKER_PREFIX}{} --> {MARKER_PREFIX}{a} --> {MARKER_PREFIX}{missing} --> {MARKER_PREFIX}{b} -->",
            a.to_uppercase()
        );
        let text = merge(&current, String::new(), None, |id| {
            if id == a {
                Some(("[a]\\b\r\n", "text/plain"))
            } else if id == b {
                Some(("", "image/png"))
            } else {
                None
            }
        });
        assert_eq!(
            text,
            format!(
                "[\\[a\\]\\\\b  ](/api/attachments/{a}/download)\n\n{MARKER_PREFIX}{a} -->\n\n![](/api/attachments/{b}/download)\n\n{MARKER_PREFIX}{b} -->"
            )
        );
    }

    #[test]
    fn known_attachment_can_be_removed_and_retained_link_restores_marker() {
        let id = "01234567-b123-4567-8999-123456789012";
        let current = format!("{MARKER_PREFIX}{id} -->");
        let file = |_: &str| Some(("image", "image/png"));
        assert_eq!(
            merge(&current, "removed".into(), Some(&current), file),
            "removed"
        );
        let link = format!("![](/api/attachments/{id}/download)");
        assert_eq!(
            merge(&current, link.clone(), Some(&current), file),
            format!("{link}\n\n{current}")
        );
    }
}
