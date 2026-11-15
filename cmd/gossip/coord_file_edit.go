package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/tasmirz/gossip/pkg/coord"
)

func runFileEdit(args []string) error {
	fs := flag.NewFlagSet("file-edit", flag.ContinueOnError)
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	profile := fs.String("profile", "default", "registered identity profile")
	project := fs.String("project", "", "registered project")
	checkout := fs.String("checkout", "", "registered checkout selector")
	handle := fs.String("handle", "", "active reservation handle")
	path := fs.String("path", "", "worktree-relative path")
	expected := fs.String("expect", "", "expected SHA-256 or absent")
	remove := fs.Bool("delete", false, "delete the existing file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *handle == "" || *path == "" || *expected == "" {
		return errors.New("file-edit requires --handle, --path, and --expect")
	}
	ctx := context.Background()
	registry, err := coord.OpenDeviceRegistry("")
	if err != nil {
		return err
	}
	defer registry.Close()
	c, token, err := registry.Resolve(ctx, *profile, *project, *checkout)
	if err != nil {
		return err
	}
	if err := verifyFileIdentity(ctx, *db, c, token); err != nil {
		return err
	}
	var content []byte
	if !*remove {
		const limit = 32 << 20
		content, err = io.ReadAll(io.LimitReader(os.Stdin, limit+1))
		if err != nil {
			return err
		}
		if len(content) > limit {
			return errors.New("replacement exceeds 32 MiB")
		}
	}
	return registry.GuardedFileEdit(ctx, c, *handle, *path, *expected, *remove, content)
}

func verifyFileIdentity(ctx context.Context, db string, c coord.DeviceConnection, token string) error {
	if c.Endpoint != "" {
		return errors.New("file editing requires a local project connection")
	}
	local, err := coord.Open(db)
	if err != nil {
		return err
	}
	defer local.Close()
	if local.InstanceID() != c.HomeID {
		return coord.ErrUnauthorized
	}
	actor, err := local.WhoAmI(ctx, c.ProjectID, token)
	if err != nil {
		return err
	}
	if actor.ID != c.PrincipalID {
		return coord.ErrUnauthorized
	}
	return nil
}
