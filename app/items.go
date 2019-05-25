package app

import (
	h "github.com/go-ap/activitypub/handler"
	"github.com/go-ap/activitypub/storage"
	as "github.com/go-ap/activitystreams"
	"github.com/go-ap/fedbox/internal/context"
	"github.com/go-ap/fedbox/internal/errors"
	st "github.com/go-ap/fedbox/storage"
	"github.com/go-chi/chi"
	"net/http"
)

// HandleItem serves content from the following, followers, liked, and likes end-points
// that returns a single ActivityPub object
func HandleItem(w http.ResponseWriter, r *http.Request) (as.Item, error) {
	collection := h.Typer.Type(r)

	id := chi.URLParam(r, "id")

	var items as.ItemCollection
	var err error
	f := &st.Filters{}
	f.FromRequest(r)
	if len(f.ItemKey) == 0 {
		f.ItemKey = []st.Hash{
			st.Hash(id),
		}
	}
	f.MaxItems = 1

	if !st.ValidActivityCollection(string(f.Collection)) {
		return nil, NotFoundf("collection '%s' not found", f.Collection)
	}

	if h.ValidObjectCollection(string(f.Collection)) {
		var repo storage.ObjectLoader
		var ok bool
		if repo, ok = context.ObjectLoader(r.Context()); !ok {
			return nil, errors.Newf("invalid object loader")
		}
		items, _, err = repo.LoadObjects(f)
	} else if st.ValidActivityCollection(string(f.Collection)) {
		var repo storage.ActivityLoader
		var ok bool
		if repo, ok = context.ActivityLoader(r.Context()); !ok {
			return nil, errors.Newf("invalid activity loader")
		}
		items, _, err = repo.LoadActivities(f)
	}

	switch f.Collection {
	case st.ActivitiesType:
		var repo storage.ActivityLoader
		var ok bool
		if repo, ok = context.ActivityLoader(r.Context()); !ok {
			return nil, errors.Newf("invalid activity loader")
		}
		items, _, err = repo.LoadActivities(f)
	case st.ActorsType:
		var repo storage.ActorLoader
		var ok bool
		if repo, ok = context.ActorLoader(r.Context()); !ok {
			return nil, errors.Newf("invalid database connection")
		}
		items, _, err = repo.LoadActors(f)
	case st.ObjectsType:
		var repo storage.ObjectLoader
		var ok bool
		if repo, ok = context.ObjectLoader(r.Context()); !ok {
			return nil, errors.Newf("invalid database connection")
		}
		items, _, err = repo.LoadObjects(f)
	default:
		return nil, errors.Newf("invalid collection %s", f.Collection)
	}
	if err != nil {
		return nil, err
	}
	if len(items) == 1 {
		it, err := loadItem(items, f, reqURL(r, r.URL.Path))
		if err != nil {
			return nil, NotFoundf("Not found %s", collection)
		}
		return it, nil
	}

	return nil, NotFoundf("Not found %s in %s", id, collection)
}

func loadItem(items as.ItemCollection, f st.Paginator, baseURL string) (as.Item, error) {
	return items[0], nil
}
