package runner

import (
	"context"
	json "encoding/json/v2"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/execenv/runnerproto"
)

// CommandOptions carries the serializable command settings.
type CommandOptions struct {
	Cwd        string            `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	InheritEnv *bool             `json:"inheritEnv,omitzero"`
	Timeout    *float64          `json:"timeout,omitzero"`
}

func readOptions[T any](request runnerproto.Request) (T, error) {
	var options T
	if len(request.Options) == 0 {
		return options, nil
	}
	return options, json.Unmarshal(request.Options, &options, json.RejectUnknownMembers(true))
}

func (s *executor) files(ctx context.Context, op *operation) error {
	r := op.request
	var value any
	var err error
	switch r.Op {
	case "stat":
		value, err = s.local.FileInfo(ctx, r.Path)
	case "canonical":
		value, err = s.local.CanonicalPath(ctx, r.Path)
	case "list":
		value, err = s.local.ListDir(ctx, r.Path)
	case "text":
		value, err = s.local.ReadTextFile(ctx, r.Path)
	case "flush":
		return s.local.FlushFile(ctx, r.Path)
	case "rename":
		dest, err := readOptions[string](r)
		if err != nil {
			return err
		}
		return s.local.RenameFile(ctx, r.Path, dest)
	case "truncate":
		size, err := readOptions[int64](r)
		if err != nil {
			return err
		}
		return s.local.TruncateFile(ctx, r.Path, size)
	case "mkdir":
		opts, err := readOptions[*env.CreateDirOptions](r)
		if err != nil {
			return err
		}
		return s.local.CreateDir(ctx, r.Path, opts)
	case "remove":
		opts, err := readOptions[*env.RemoveOptions](r)
		if err != nil {
			return err
		}
		return s.local.Remove(ctx, r.Path, opts)
	case "temp-dir":
		opts, err := readOptions[*string](r)
		if err != nil {
			return err
		}
		value, err = s.local.CreateTempDir(ctx, opts)
		if err != nil {
			return err
		}
	case "temp-file":
		opts, err := readOptions[*env.CreateTempFileOptions](r)
		if err != nil {
			return err
		}
		value, err = s.local.CreateTempFile(ctx, opts)
		if err != nil {
			return err
		}
	default:
		return errProtocol
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxFileBytes {
		return errFileLimit
	}
	return writeChunks(ctx, s.wire, op.id, data)
}
