// +build storage_fs

package app

import (
	"github.com/go-ap/auth"
	"github.com/go-ap/fedbox/internal/config"
	"github.com/go-ap/fedbox/storage/fs"
	st "github.com/go-ap/storage"
	"github.com/openshift/osin"
	"github.com/sirupsen/logrus"
)

func getFsStorage(c config.Options, l logrus.FieldLogger) (st.Repository, osin.Storage, error) {
	oauth := auth.NewFSStore(auth.FSConfig{
		Path:  c.BaseStoragePath(),
		LogFn: InfoLogFn(l),
		ErrFn: ErrLogFn(l),
	})
	db, err := fs.New(c)
	if err != nil {
		return nil, oauth, err
	}
	return db, oauth, nil
}

func Storage(c config.Options, l logrus.FieldLogger) (st.Repository, osin.Storage, error) {
	return getFsStorage(c, l)
}