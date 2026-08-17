package fleet

import (
	"io/fs"
	"time"
)

type testFileInfo struct{ mode fs.FileMode }

func (info testFileInfo) Name() string       { return "test" }
func (info testFileInfo) Size() int64        { return 0 }
func (info testFileInfo) Mode() fs.FileMode  { return info.mode }
func (info testFileInfo) ModTime() time.Time { return time.Time{} }
func (info testFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info testFileInfo) Sys() any           { return nil }
