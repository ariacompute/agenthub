fn main() {
    // rust-embed lists the folder at compile time and refuses to build when it
    // is missing. `backend/static` is a build artefact and is gitignored, so
    // guarantee it exists before the macro runs: an empty directory embeds
    // nothing, and the shell then shows its own "console is missing" page
    // instead of failing to compile.
    let console = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../backend/static");
    if !console.exists() {
        let _ = std::fs::create_dir_all(&console);
    }
    tauri_build::build()
}
