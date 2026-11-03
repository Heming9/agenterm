//! Files are staged in the destination directory and published without replacement.
use sha2::{Digest, Sha256};
use std::{
    fs::{self, File, OpenOptions},
    io::{self, Read, Write},
    path::{Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
};

pub fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

pub struct Incoming {
    file: Option<File>,
    temp: PathBuf,
    destination: PathBuf,
    pub bytes: u64,
    size: u64,
    overwrite: bool,
    pub committed: Arc<AtomicBool>,
}

impl Incoming {
    #[cfg(test)]
    pub fn create(destination: &Path, size: u64, limit: u64) -> io::Result<Self> {
        Self::create_with_overwrite(destination, size, limit, false)
    }
    pub fn create_with_overwrite(
        destination: &Path,
        size: u64,
        limit: u64,
        overwrite: bool,
    ) -> io::Result<Self> {
        if size > limit {
            return Err(io::Error::other("file exceeds receive limit"));
        }
        // symlink_metadata also detects dangling symlinks.
        match fs::symlink_metadata(destination) {
            Ok(meta) if overwrite && meta.file_type().is_file() => {}
            Ok(_) => {
                return Err(io::Error::new(
                    io::ErrorKind::AlreadyExists,
                    "destination exists",
                ));
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => {}
            Err(e) => return Err(e),
        }
        let parent = destination
            .parent()
            .filter(|p| !p.as_os_str().is_empty())
            .unwrap_or(Path::new("."));
        let mut nonce = [0; 16];
        getrandom::fill(&mut nonce).map_err(io::Error::other)?;
        let temp = parent.join(format!(".agenterm-{}.part", hex(&nonce)));
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        let file = options.open(&temp)?;
        Ok(Self {
            file: Some(file),
            temp,
            destination: destination.to_path_buf(),
            bytes: 0,
            size,
            overwrite,
            committed: Arc::new(AtomicBool::new(false)),
        })
    }

    pub fn write(&mut self, data: &[u8]) -> io::Result<()> {
        if data.is_empty() || data.len() > 16 * 1024 || self.bytes + data.len() as u64 > self.size {
            return Err(io::Error::other(
                "invalid file chunk or declared size exceeded",
            ));
        }
        self.file
            .as_mut()
            .ok_or_else(|| io::Error::other("file already closed"))?
            .write_all(data)?;
        self.bytes += data.len() as u64;
        Ok(())
    }

    // Runs off the RTC thread: fsync and rereading a large file must not stall ICE.
    pub fn verify_commit(mut self, expected: &str, canceled: &AtomicBool) -> io::Result<String> {
        if self.bytes != self.size {
            return Err(io::Error::other("incomplete file"));
        }
        self.file
            .take()
            .ok_or_else(|| io::Error::other("file already closed"))?
            .sync_all()?;
        let mut reader = File::open(&self.temp)?;
        let mut hash = Sha256::new();
        let mut buffer = [0; 64 * 1024];
        loop {
            if canceled.load(Ordering::Relaxed) {
                return Err(io::Error::other("canceled"));
            }
            let n = reader.read(&mut buffer)?;
            if n == 0 {
                break;
            }
            hash.update(&buffer[..n]);
        }
        drop(reader);
        let actual = hex(&hash.finalize());
        if actual != expected {
            return Err(io::Error::other("SHA-256 mismatch"));
        }
        if canceled.load(Ordering::Relaxed) {
            return Err(io::Error::other("canceled"));
        }
        // Same-directory hard link is atomic and fails if the destination now exists.
        // Unlike rename(), this cannot silently overwrite a file created during transfer.
        if self.overwrite {
            // Never replace a symlink/directory, even with an explicit overwrite.
            if let Ok(meta) = fs::symlink_metadata(&self.destination)
                && !meta.file_type().is_file()
            {
                return Err(io::Error::other("destination is not a regular file"));
            }
            replace_file(&self.temp, &self.destination)?;
        } else {
            fs::hard_link(&self.temp, &self.destination)?;
        }
        self.committed.store(true, Ordering::Release);
        if !self.overwrite {
            fs::remove_file(&self.temp)?;
        }
        #[cfg(unix)]
        {
            let parent = self
                .destination
                .parent()
                .filter(|p| !p.as_os_str().is_empty())
                .unwrap_or(Path::new("."));
            File::open(parent)?.sync_all()?;
        }
        Ok(actual)
    }
}

#[cfg(not(windows))]
fn replace_file(source: &Path, target: &Path) -> io::Result<()> {
    fs::rename(source, target)
}
#[cfg(windows)]
fn replace_file(source: &Path, target: &Path) -> io::Result<()> {
    use std::os::windows::ffi::OsStrExt;
    use windows_sys::Win32::Storage::FileSystem::{
        MOVEFILE_REPLACE_EXISTING, MOVEFILE_WRITE_THROUGH, MoveFileExW,
    };
    let source: Vec<u16> = source.as_os_str().encode_wide().chain(Some(0)).collect();
    let target: Vec<u16> = target.as_os_str().encode_wide().chain(Some(0)).collect();
    if unsafe {
        MoveFileExW(
            source.as_ptr(),
            target.as_ptr(),
            MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH,
        )
    } == 0
    {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

impl Drop for Incoming {
    fn drop(&mut self) {
        drop(self.file.take()); // Windows cannot unlink an open file.
        let _ = fs::remove_file(&self.temp);
    }
}

pub struct Verification {
    worker: Option<std::thread::JoinHandle<io::Result<String>>>,
    canceled: Arc<AtomicBool>,
}
impl Verification {
    pub fn start(file: Incoming, hash: String, canceled: Arc<AtomicBool>) -> Self {
        let flag = canceled.clone();
        Self {
            worker: Some(std::thread::spawn(move || file.verify_commit(&hash, &flag))),
            canceled,
        }
    }
    pub fn result(&mut self) -> Option<io::Result<String>> {
        if !self.worker.as_ref()?.is_finished() {
            return None;
        }
        self.wait()
    }
    pub fn wait(&mut self) -> Option<io::Result<String>> {
        Some(
            self.worker
                .take()?
                .join()
                .unwrap_or_else(|_| Err(io::Error::other("verification worker panicked"))),
        )
    }
}
impl Drop for Verification {
    fn drop(&mut self) {
        if let Some(worker) = self.worker.take() {
            self.canceled.store(true, Ordering::Relaxed);
            let _ = worker.join();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn directory() -> PathBuf {
        let mut nonce = [0; 8];
        getrandom::fill(&mut nonce).unwrap();
        let path = std::env::temp_dir().join(format!("agenterm-file-test-{}", hex(&nonce)));
        fs::create_dir(&path).unwrap();
        path
    }
    #[test]
    fn overwrite_preserves_original_until_verified_and_committed() {
        let dir = directory();
        let dest = dir.join("target");
        fs::write(&dest, b"old").unwrap();
        let mut incoming = Incoming::create_with_overwrite(&dest, 3, 3, true).unwrap();
        incoming.write(b"new").unwrap();
        assert_eq!(fs::read(&dest).unwrap(), b"old");
        assert!(
            incoming
                .verify_commit(&hex(&Sha256::digest(b"bad")), &AtomicBool::new(false))
                .is_err()
        );
        assert_eq!(fs::read(&dest).unwrap(), b"old");
        let mut incoming = Incoming::create_with_overwrite(&dest, 3, 3, true).unwrap();
        incoming.write(b"new").unwrap();
        incoming
            .verify_commit(&hex(&Sha256::digest(b"new")), &AtomicBool::new(false))
            .unwrap();
        assert_eq!(fs::read(&dest).unwrap(), b"new");
        assert_eq!(fs::read_dir(&dir).unwrap().count(), 1);
        fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn verifies_disk_contents_and_preserves_racing_destination() {
        let dir = directory();
        let dest = dir.join("received");
        let mut file = Incoming::create(&dest, 3, 3).unwrap();
        file.write(b"abc").unwrap();
        fs::write(&dest, b"keep").unwrap();
        let hash = hex(&Sha256::digest(b"abc"));
        assert!(file.verify_commit(&hash, &AtomicBool::new(false)).is_err());
        assert_eq!(fs::read(&dest).unwrap(), b"keep");
        assert_eq!(fs::read_dir(&dir).unwrap().count(), 1);
        fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn corrupt_incomplete_and_canceled_files_are_removed() {
        for case in 0..3 {
            let dir = directory();
            let dest = dir.join("received");
            let mut file = Incoming::create(&dest, 3, 3).unwrap();
            file.write(if case == 1 { b"ab" } else { b"abc" }).unwrap();
            let hash = hex(&Sha256::digest(if case == 0 { b"bad" } else { b"abc" }));
            assert!(
                file.verify_commit(&hash, &AtomicBool::new(case == 2))
                    .is_err()
            );
            assert_eq!(fs::read_dir(&dir).unwrap().count(), 0);
            fs::remove_dir(dir).unwrap();
        }
    }
    #[test]
    fn commits_empty_and_nonempty_files_and_enforces_limit() {
        for data in [b"".as_slice(), b"abc".as_slice()] {
            let dir = directory();
            let dest = dir.join("received");
            assert!(Incoming::create(&dest, 4, 3).is_err());
            let mut file = Incoming::create(&dest, data.len() as u64, 3).unwrap();
            if !data.is_empty() {
                file.write(data).unwrap();
            }
            assert!(file.write(b"extra").is_err());
            let hash = hex(&Sha256::digest(data));
            assert_eq!(
                file.verify_commit(&hash, &AtomicBool::new(false)).unwrap(),
                hash
            );
            assert_eq!(fs::read(&dest).unwrap(), data);
            assert!(Incoming::create(&dest, 0, 3).is_err());
            fs::remove_dir_all(dir).unwrap();
        }
    }
}
