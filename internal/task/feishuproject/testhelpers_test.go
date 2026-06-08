package feishuproject_test

import "os"

func writeAtomic(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o644)
}
