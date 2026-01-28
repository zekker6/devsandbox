package sandbox

import (
	"os"
	"os/exec"
	"strings"
)

func generateSafeGitconfig(path string) error {
	var sb strings.Builder

	userName := getGitConfig("user.name")
	userEmail := getGitConfig("user.email")

	sb.WriteString("# Safe git config for sandbox (no credentials)\n")

	if userName != "" || userEmail != "" {
		sb.WriteString("[user]\n")
		if userName != "" {
			sb.WriteString("    name = ")
			sb.WriteString(userName)
			sb.WriteString("\n")
		}
		if userEmail != "" {
			sb.WriteString("    email = ")
			sb.WriteString(userEmail)
			sb.WriteString("\n")
		}
	}

	sb.WriteString("[core]\n")
	sb.WriteString("    editor = nvim\n")

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

func getGitConfig(key string) string {
	cmd := exec.Command("git", "config", "--global", key)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
