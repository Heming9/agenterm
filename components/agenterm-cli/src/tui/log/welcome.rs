use super::{Kind, Metrics, Row};
use crate::{deeplink::hash_reader, protocol::VERSION, tui::colors, ui::tr};
use std::{env, fs::File, sync::OnceLock};

pub(crate) const COMMIT: &str = env!("AGENTERM_COMMIT");
pub(crate) const HASH_SEPARATOR: &str = "......";
const PROJECT_URL: &str = "https://github.com/Heming9/agenterm";

pub(crate) fn executable_hash() -> &'static str {
    static HASH: OnceLock<Option<String>> = OnceLock::new();
    HASH.get_or_init(|| {
        let mut file = File::open(env::current_exe().ok()?).ok()?;
        let digest = hash_reader(&mut file).ok()?;
        Some(format!(
            "{}{HASH_SEPARATOR}{}",
            &digest[..8],
            &digest[digest.len() - 8..]
        ))
    })
    .as_deref()
    .unwrap_or("unavailable")
}

pub(super) fn rows(width: usize, height: usize, metrics: Metrics) -> Vec<Row> {
    let mut rows = Vec::new();
    if height > 0 {
        let release = format!("Agenterm v{VERSION}");
        let title = format!("{release}.{COMMIT}");
        let greeting = tr("tui.welcome.greeting");
        let welcome = format!("{greeting} {title}");
        let hash = format!("sha256: {}", executable_hash());
        if metrics.width(&welcome) + 9 <= width && PROJECT_URL.len() + 11 <= width && height >= 3 {
            let [top, middle, bottom] = colors::TERMINAL_LOGO;
            rows.push(line(format!("  {top}  {welcome}"), "", Kind::Welcome));
            rows.push(line(
                format!("  {middle}"),
                &format!("   - {hash}"),
                Kind::Welcome,
            ));
            rows.push(line(
                format!("  {bottom}"),
                &format!("  - {PROJECT_URL}"),
                Kind::Welcome,
            ));
        } else {
            let title = [welcome, format!("{greeting} {release}"), title]
                .into_iter()
                .find(|title| metrics.width(title) <= width)
                .unwrap_or_else(|| metrics.truncate(&release, width, ""));
            rows.push(line(format!(" {title}"), "", Kind::Welcome));
            if height >= 2 && metrics.width(&hash) <= width {
                rows.push(line(format!(" {hash}"), "", Kind::Output));
            } else if height >= 3
                && width >= 16
                && let Some((head, tail)) = executable_hash().split_once(HASH_SEPARATOR)
            {
                rows.push(line(format!(" sha256: {head}"), "", Kind::Output));
                rows.push(line(format!(" {HASH_SEPARATOR}{tail}"), "", Kind::Output));
            }
            if rows.len() < height && PROJECT_URL.len() <= width {
                rows.push(line(format!(" {PROJECT_URL}"), "", Kind::Output));
            }
        }
    }
    rows
}

fn line(mark: String, rest: &str, kind: Kind) -> Row {
    Row {
        suffix: mark.len(),
        text: mark + rest,
        kind,
        suffix_kind: Kind::Output,
        ..Row::blank()
    }
}
