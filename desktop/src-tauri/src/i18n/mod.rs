//! Language resolution for the desktop shell.
//!
//! The console carries its own 13-language copy; this module exists for the two
//! or three screens the shell itself owns (the tray menu and the connection
//! page). It mirrors `web/src/lib/languages.js`: the same 13 codes, the same
//! `zh`/`zh-TW` split, the same English fallback.

pub mod copy;

pub use copy::LANGUAGES;

/// Only Arabic reads right to left.
pub fn is_rtl(code: &str) -> bool {
    code == "ar"
}

/// True for the 13 codes the console supports.
pub fn is_valid_lang(code: &str) -> bool {
    lang_code(code).is_some()
}

/// Maps a code onto its `&'static str` twin, so callers can hold one without
/// allocating.
pub fn lang_code(code: &str) -> Option<&'static str> {
    LANGUAGES.iter().find(|(known, _)| *known == code).map(|(known, _)| *known)
}

/// Maps a raw platform locale onto one of the 13 codes, exactly like
/// `matchLang` in the console.
pub fn match_lang(raw: &str) -> &'static str {
    if raw.is_empty() {
        return "en";
    }
    if let Some(code) = lang_code(raw) {
        return code;
    }
    let lower = raw.to_lowercase();
    if lower.starts_with("zh") {
        let traditional = lower.contains("tw") || lower.contains("hk") || lower.contains("hant");
        return if traditional { "zh-TW" } else { "zh" };
    }
    let base = lower.split('-').next().unwrap_or("");
    lang_code(base).unwrap_or("en")
}

/// The language the machine asks for; English when it asks for something we do
/// not ship.
pub fn detect_system_lang() -> &'static str {
    match sys_locale::get_locale() {
        Some(raw) => match_lang(&raw),
        None => "en",
    }
}

/// One key in one language, falling back to English and then to the empty
/// string (so a missing key renders as nothing rather than as the key name).
pub fn translate(lang: &str, key: &str) -> &'static str {
    lookup(lang, key).unwrap_or("")
}

fn lookup(lang: &str, key: &str) -> Option<&'static str> {
    let in_lang = copy::block(lang).iter().find(|(k, _)| *k == key).map(|(_, v)| *v);
    if in_lang.is_some() {
        return in_lang;
    }
    copy::block("en").iter().find(|(k, _)| *k == key).map(|(_, v)| *v)
}

/// The whole block for one language, English fallback per key, ready to hand to
/// the connection page as JSON.
pub fn block_for(lang: &str) -> Vec<(&'static str, &'static str)> {
    copy::keys()
        .map(|key| (key, translate(lang, key)))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    const CODES: [&str; 13] = [
        "en", "zh", "zh-TW", "es", "fr", "de", "it", "ru", "ja", "ko", "pt", "ar", "hi",
    ];

    #[test]
    fn ships_exactly_the_thirteen_console_languages() {
        assert_eq!(LANGUAGES.len(), 13);
        assert_eq!(LANGUAGES.map(|(code, _)| code), CODES);
    }

    #[test]
    fn marks_only_arabic_as_right_to_left() {
        assert!(is_rtl("ar"));
        for code in CODES {
            if code != "ar" {
                assert!(!is_rtl(code), "{code} should read left to right");
            }
        }
    }

    #[test]
    fn validates_language_codes() {
        assert!(is_valid_lang("zh-TW"));
        assert!(!is_valid_lang("nl"));
        assert!(!is_valid_lang(""));
    }

    #[test]
    fn maps_raw_platform_locales_onto_supported_codes() {
        assert_eq!(match_lang(""), "en");
        assert_eq!(match_lang("en-US"), "en");
        assert_eq!(match_lang("zh-CN"), "zh");
        assert_eq!(match_lang("zh-TW"), "zh-TW");
        assert_eq!(match_lang("zh-Hant-HK"), "zh-TW");
        assert_eq!(match_lang("fr-CA"), "fr");
        assert_eq!(match_lang("nl-NL"), "en");
    }

    #[test]
    fn gives_every_language_its_own_translation_of_every_key() {
        for key in copy::keys() {
            let english = translate("en", key);
            assert!(!english.is_empty(), "{key} has no English text");
            for code in CODES {
                let text = translate(code, key);
                assert!(!text.is_empty(), "{code}.{key} is empty");
                if code != "en" {
                    assert_ne!(text, english, "{code}.{key} merely repeats English");
                }
            }
        }
    }

    #[test]
    fn falls_back_to_english_for_an_unsupported_language() {
        for key in copy::keys() {
            assert_eq!(translate("nl", key), translate("en", key), "nl.{key}");
        }
    }

    #[test]
    fn returns_empty_for_a_key_that_does_not_exist() {
        for code in CODES {
            assert_eq!(translate(code, "thisKeyDoesNotExist"), "");
        }
    }
}
