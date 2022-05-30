//go:build storage_sqlite
// +build storage_sqlite

package app

import (
	auth "github.com/go-ap/auth/sqlite"
	"github.com/go-ap/errors"
	"github.com/go-ap/fedbox/internal/config"
	"github.com/go-ap/fedbox/storage/sqlite"
	"github.com/go-ap/processing"
	"github.com/openshift/osin"
	"github.com/sirupsen/logrus"
)

func Storage(c config.Options, l logrus.FieldLogger) (processing.Store, osin.Storage, error) {
	path := c.BaseStoragePath()
	l.Debugf("Initializing sqlite storage at %s", path)
	oauth := auth.New(auth.Config{
		Path:  path,
		LogFn: InfoLogFn(l),
		ErrFn: ErrLogFn(l),
	})
	db, err := sqlite.New(sqlite.Config{
		StoragePath: path,
		BaseURL:     c.BaseURL,
		EnableCache: c.StorageCache,
	})

	if err != nil {
		return nil, nil, errors.Annotatef(err, "unable to connect to sqlite storage")
	}
	return db, oauth, nil
}
