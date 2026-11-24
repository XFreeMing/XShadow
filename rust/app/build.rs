use std::{env, fs, io, path::PathBuf};

fn main() -> io::Result<()> {
    let migrations = "../../server/migrations";
    println!("cargo:rerun-if-changed={migrations}");
    println!("cargo:rerun-if-env-changed=XSHADOW_BUILD_COMMIT");

    let mut versions = Vec::new();
    for entry in fs::read_dir(migrations)? {
        let entry = entry?;
        let name = entry.file_name().into_string().map_err(|_| {
            io::Error::new(io::ErrorKind::InvalidData, "invalid migration filename")
        })?;
        if let Some(version) = name.strip_suffix(".up.sql") {
            versions.push(version.to_owned());
        }
    }
    versions.sort_unstable();
    if versions.is_empty() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "no up migrations found",
        ));
    }
    let output = PathBuf::from(
        env::var_os("OUT_DIR")
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "Cargo did not set OUT_DIR"))?,
    );
    fs::write(output.join("migration-versions.txt"), versions.join("\n"))
}
