package coord

import (
	"golang.org/x/sys/windows"
	"os"
)

func ordinaryPath(path string, info os.FileInfo) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var data windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(h, &data) == nil &&
		data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 &&
		(info.IsDir() || data.NumberOfLinks == 1)
}

func replaceGuardedFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
