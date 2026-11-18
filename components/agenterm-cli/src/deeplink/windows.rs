use super::*;
use std::{ffi::OsStr, os::windows::ffi::OsStrExt};
use windows_sys::Win32::System::Registry::{
    self, HKEY, HKEY_CURRENT_USER, KEY_SET_VALUE, REG_OPTION_NON_VOLATILE, REG_SZ,
};

struct Key(HKEY);
impl Drop for Key {
    fn drop(&mut self) {
        unsafe { Registry::RegCloseKey(self.0) };
    }
}
fn wide(s: &OsStr) -> Vec<u16> {
    s.encode_wide().chain(Some(0)).collect()
}
fn set(key_path: &str, name: &str, value: &OsStr) -> Result<()> {
    let mut handle = std::ptr::null_mut();
    let path = wide(OsStr::new(key_path));
    let code = unsafe {
        Registry::RegCreateKeyExW(
            HKEY_CURRENT_USER,
            path.as_ptr(),
            0,
            std::ptr::null(),
            REG_OPTION_NON_VOLATILE,
            KEY_SET_VALUE,
            std::ptr::null(),
            &mut handle,
            std::ptr::null_mut(),
        )
    };
    if code != 0 {
        return Err(std::io::Error::from_raw_os_error(code as i32).to_string());
    }
    let key = Key(handle);
    let name = wide(OsStr::new(name));
    let value = wide(value);
    let code = unsafe {
        Registry::RegSetValueExW(
            key.0,
            name.as_ptr(),
            0,
            REG_SZ,
            value.as_ptr().cast(),
            (value.len() * 2) as u32,
        )
    };
    if code != 0 {
        return Err(std::io::Error::from_raw_os_error(code as i32).to_string());
    }
    Ok(())
}
pub(super) fn register(executable: &Path) -> Result<()> {
    let path = r"Software\Classes\agenterm-cli";
    set(path, "", OsStr::new("URL:Agenterm CLI Protocol"))?;
    set(path, "URL Protocol", OsStr::new(""))?;
    let mut command = std::ffi::OsString::from("\"");
    command.push(executable);
    command.push("\" \"%1\"");
    set(&format!(r"{path}\shell\open\command"), "", &command)
}
