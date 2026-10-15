package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
)

const maxConfigFileSize = 1024 * 1024

func parseDotEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read configuration")
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxConfigFileSize+1))
	if err != nil {
		return nil, errors.New("cannot read configuration")
	}
	if len(data) > maxConfigFileSize {
		return nil, errors.New("configuration exceeds size limit")
	}
	return parseDotEnv(data)
}

func parseDotEnv(data []byte) (map[string]string, error) {
	result := make(map[string]string)
	lines := bytes.Split(data, []byte("\n"))
	for _, rawLine := range lines {
		line := strings.TrimRight(string(rawLine), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "export\t") {
			trimmed = strings.TrimSpace(trimmed[6:])
		}
		idx := strings.Index(trimmed, "=")
		if idx == -1 {
			return nil, errors.New("invalid configuration environment file")
		}
		key := strings.TrimSpace(trimmed[:idx])
		if key == "" || strings.ContainsAny(key, " \t\r\n") {
			return nil, errors.New("invalid configuration environment file")
		}
		rawVal := strings.TrimSpace(trimmed[idx+1:])
		if rawVal == "" {
			result[key] = ""
			continue
		}
		if strings.HasPrefix(rawVal, "'") {
			closing := strings.Index(rawVal[1:], "'")
			if closing == -1 {
				return nil, errors.New("invalid configuration environment file")
			}
			val := rawVal[1 : 1+closing]
			rest := strings.TrimSpace(rawVal[1+closing+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return nil, errors.New("invalid configuration environment file")
			}
			result[key] = val
		} else if strings.HasPrefix(rawVal, "\"") {
			var valBuilder strings.Builder
			escaped := false
			closed := false
			closingIdx := -1
			for i := 1; i < len(rawVal); i++ {
				c := rawVal[i]
				if escaped {
					switch c {
					case 'n':
						valBuilder.WriteByte('\n')
					case 't':
						valBuilder.WriteByte('\t')
					case 'r':
						valBuilder.WriteByte('\r')
					case '"':
						valBuilder.WriteByte('"')
					case '\\':
						valBuilder.WriteByte('\\')
					case '$':
						valBuilder.WriteByte('$')
					default:
						valBuilder.WriteByte('\\')
						valBuilder.WriteByte(c)
					}
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == '"' {
					closed = true
					closingIdx = i
					break
				} else {
					valBuilder.WriteByte(c)
				}
			}
			if !closed || escaped {
				return nil, errors.New("invalid configuration environment file")
			}
			rest := strings.TrimSpace(rawVal[closingIdx+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return nil, errors.New("invalid configuration environment file")
			}
			result[key] = valBuilder.String()
		} else {
			afterEqual := trimmed[idx+1:]
			commentStart := -1
			for i := 0; i < len(afterEqual); i++ {
				if afterEqual[i] == '#' {
					if i > 0 && (afterEqual[i-1] == ' ' || afterEqual[i-1] == '\t') {
						commentStart = i
						break
					}
				}
			}
			var val string
			if commentStart != -1 {
				val = strings.TrimSpace(afterEqual[:commentStart])
			} else {
				val = strings.TrimSpace(afterEqual)
			}
			result[key] = val
		}
	}
	return result, nil
}
