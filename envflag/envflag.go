// envflag wraps flag.FlagSet,
// allowing values to be set from the environment,
// translating keys from
// `screaming.snake-case` to `SCREAMING_SNAKE_CASE`
package envflag

import (
	"flag"
	"fmt"
	"io"
	"path"
	"runtime/debug"
	"strings"
)

type Config struct {
	*flag.FlagSet
}

func New(name string, out io.Writer) *Config {
	if name == "" {
		bi, ok := debug.ReadBuildInfo()
		if ok {
			name = path.Base(bi.Path)
			// check if last path element matches vXXX for major version
			if name[0] == 'v' {
				var notDigit bool
				for _, r := range name[1:] {
					if r < '0' || r > '9' {
						notDigit = true
					}
				}
				if !notDigit {
					name = path.Base(path.Dir(bi.Path))
				}
			}
		}
	}
	c := &Config{
		FlagSet: flag.NewFlagSet(name, flag.ContinueOnError),
	}
	c.SetOutput(out)
	return c
}

func (c *Config) Parse(args, env []string) error {
	mapEnv := make(map[string]string)
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		mapEnv[k] = v
	}

	var errs []setEnvErr
	c.VisitAll(func(f *flag.Flag) {
		k := strings.ToUpper(f.Name)
		k = strings.ReplaceAll(k, ".", "_")
		k = strings.ReplaceAll(k, "-", "_")
		if v, ok := mapEnv[k]; ok {
			err := f.Value.Set(v)
			if err != nil {
				errs = append(errs, setEnvErr{k, v, err})
			}
		}
	})
	if len(errs) > 0 {
		return fmt.Errorf("envflag: set flag from env: %v", errs)
	}
	err := c.FlagSet.Parse(args)
	if err != nil {
		return fmt.Errorf("envflag: set flag from arg: %w", err)
	}
	return nil
}

type setEnvErr struct {
	name, value string
	err         error
}

func (s setEnvErr) Error() string {
	return fmt.Sprintf("%s=%s: %v", s.name, s.value, s.err)
}
