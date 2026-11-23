package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"github.com/multica-ai/multica/server/internal/testutil"
	"image"
	"image/png"
	"net/http"
	"reflect"
	"testing"
)

func TestGoalOutcomeGallery(t *testing.T) {
	f := newPrivateProjectFixture(t)
	g := createGoalViaAPI(t, f.insider, map[string]any{"title": "Book goal", "project_id": f.projectID})
	params := map[string]string{"id": g.ID}
	path := "/api/goals/" + g.ID + "/outcome-images"
	get := func(user string, status int) []goalFrame {
		r := testutil.Call(t, testHandler.GetGoalOutcomeImages, goalRequest(http.MethodGet, path, user, nil, params)).Want(status)
		var frames []goalFrame
		if status == 200 {
			r.JSON(&frames)
		}
		return frames
	}
	put := func(user string, body any, status int) {
		testutil.Call(t, testHandler.PutGoalOutcomeImages, goalRequest(http.MethodPut, path, user, body, params)).Want(status)
	}
	if got := get(f.insider, 200); got == nil || len(got) != 0 {
		t.Fatal("expected empty gallery")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	src := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	frames := []goalFrame{{"Graph", src}, {"Answer", src}}
	put(f.insider, frames, 204)
	get(f.outsider, 404)
	put(f.outsider, []goalFrame{}, 404)
	put(f.insider, []goalFrame{frames[0], {"bad", "broken"}}, 400)
	if got := get(f.insider, 200); !reflect.DeepEqual(got, frames) {
		t.Fatal("gallery order or atomic replacement failed")
	}
	testutil.Call(t, testHandler.DeleteGoal, goalRequest(http.MethodDelete, "/api/goals/"+g.ID, f.insider, nil, params)).Want(204)
	var count int
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM goal_outcome_gallery WHERE goal_id=$1", g.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("gallery cleanup %d, %v", count, err)
	}
}
