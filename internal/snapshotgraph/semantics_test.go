package snapshotgraph

import (
	"reflect"
	"testing"
)

func TestReachableSets(t *testing.T) {
	g := &Graph{Objects: []Object{
		{ID: 0, Size: 8, Children: []ObjectID{2, 2}},
		{ID: 1, Size: 8, Children: []ObjectID{2}},
		{ID: 2, Size: 8, Children: []ObjectID{3}},
		{ID: 3, Size: 8, Children: []ObjectID{2}},
		{ID: 4, Size: 8},
	}}
	for _, tc := range []struct {
		roots []RootRef
		want  map[ObjectID]struct{}
	}{
		{[]RootRef{{ObjectID: 0}}, map[ObjectID]struct{}{0: {}, 2: {}, 3: {}}},
		{[]RootRef{{ObjectID: 1}}, map[ObjectID]struct{}{1: {}, 2: {}, 3: {}}},
		{[]RootRef{{ObjectID: 0}, {ObjectID: 1}}, map[ObjectID]struct{}{0: {}, 1: {}, 2: {}, 3: {}}},
		{[]RootRef{{ObjectID: 4}}, map[ObjectID]struct{}{4: {}}},
		{[]RootRef{{ObjectID: ObjectID(1<<32 - 1)}}, map[ObjectID]struct{}{}},
	} {
		if got := ReachableFrom(g, tc.roots); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("got %v, want %v", got, tc.want)
		}
	}
}
