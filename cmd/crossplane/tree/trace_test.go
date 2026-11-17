package tree

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane/cli/v2/cmd/crossplane/tree/internal/printer"
	"github.com/crossplane/cli/v2/pkg/resource"
)

func TestCmd_getResourceAndName(t *testing.T) {
	type args struct {
		Resource string
		Name     string
	}

	type want struct {
		resource string
		name     string
		err      error
	}

	tests := map[string]struct {
		reason string
		fields args
		want   want
	}{
		"Splitted": {
			reason: "Should return the resource and name if both are provided",
			fields: args{
				Resource: "resource",
				Name:     "name",
			},
			want: want{
				resource: "resource",
				name:     "name",
				err:      nil,
			},
		},
		"Empty": {
			// should never happen, resource is required by kong
			reason: "Should return an error if no resource is provided",
			fields: args{
				Resource: "",
				Name:     "",
			},
			want: want{
				err: errors.New(errInvalidResource),
			},
		},
		"Combined": {
			reason: "Should return the resource and name if both are provided combined as resource",
			fields: args{
				Resource: "resource/name",
				Name:     "",
			},
			want: want{
				resource: "resource",
				name:     "name",
			},
		},
		"MoreSlashes": {
			reason: "Should return an error if the resource contains more than one slashes",
			fields: args{
				Resource: "resource/name/other",
				Name:     "",
			},
			want: want{
				err: errors.New(errInvalidResource),
			},
		},
		"BothAndCombined": {
			reason: "Should return an error if a name is provided both in the resource and separately",
			fields: args{
				Resource: "resource/name",
				Name:     "name",
			},
			want: want{
				err: errors.New(errNameDoubled),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := &TraceCmd{
				Resource: tt.fields.Resource,
				Name:     tt.fields.Name,
			}

			gotResource, gotName, err := c.getResourceAndName()
			if diff := cmp.Diff(tt.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("TraceCmd.getResourceAndName() error = %v, wantErr %v", err, tt.want.err)
			}

			if diff := cmp.Diff(tt.want.resource, gotResource); diff != "" {
				t.Errorf("TraceCmd.getResourceAndName() resource = %v, want %v", gotResource, tt.want.resource)
			}

			if diff := cmp.Diff(tt.want.name, gotName); diff != "" {
				t.Errorf("TraceCmd.getResourceAndName() name = %v, want %v", gotName, tt.want.name)
			}
		})
	}
}

// fakePrinter writes the names of the resources it is asked to print.
type fakePrinter struct {
	err error
}

func (p *fakePrinter) Print(w io.Writer, r *resource.Resource) error {
	if p.err != nil {
		return p.err
	}
	_, err := fmt.Fprintf(w, "single: %s\n", r.Unstructured.GetName())
	return err
}

func (p *fakePrinter) PrintList(w io.Writer, l *resource.ResourceList) error {
	if p.err != nil {
		return p.err
	}
	for _, r := range l.Items {
		if _, err := fmt.Fprintf(w, "list: %s\n", r.Unstructured.GetName()); err != nil {
			return err
		}
	}
	return nil
}

func TestPrintResources(t *testing.T) {
	errBoom := errors.New("boom")

	type args struct {
		printer printer.Printer
		items   []*resource.Resource
		asList  bool
		watch   bool
	}

	type want struct {
		output string
		err    error
	}

	tests := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Named": {
			reason: "Should print the single named resource",
			args: args{
				printer: &fakePrinter{},
				items:   []*resource.Resource{newTestResource("root", nil)},
			},
			want: want{
				output: "single: root\n",
			},
		},
		"NamedNoItems": {
			reason: "Should print nothing if there is no named resource",
			args: args{
				printer: &fakePrinter{},
			},
		},
		"List": {
			reason: "Should print all resources as a list",
			args: args{
				printer: &fakePrinter{},
				items:   []*resource.Resource{newTestResource("a", nil), newTestResource("b", nil)},
				asList:  true,
			},
			want: want{
				output: "list: a\nlist: b\n",
			},
		},
		"ListWatch": {
			reason: "Should print the list and an error message if watch was requested for a list",
			args: args{
				printer: &fakePrinter{},
				items:   []*resource.Resource{newTestResource("a", nil)},
				asList:  true,
				watch:   true,
			},
			want: want{
				output: "list: a\nerror: you may only watch a single resource at a time\n",
			},
		},
		"NamedPrintError": {
			reason: "Should return an error if printing the named resource fails",
			args: args{
				printer: &fakePrinter{err: errBoom},
				items:   []*resource.Resource{newTestResource("root", nil)},
			},
			want: want{
				err: errors.Wrap(errBoom, errCliOutput),
			},
		},
		"ListPrintError": {
			reason: "Should return an error if printing the list fails",
			args: args{
				printer: &fakePrinter{err: errBoom},
				items:   []*resource.Resource{newTestResource("a", nil)},
				asList:  true,
			},
			want: want{
				err: errors.Wrap(errBoom, errCliOutput),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer

			err := printResources(&buf, tc.args.printer, &resource.ResourceList{Items: tc.args.items}, tc.args.asList, tc.args.watch)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nprintResources(...): -want error, +got error:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.output, buf.String()); diff != "" {
				t.Errorf("\n%s\nprintResources(...): -want output, +got output:\n%s", tc.reason, diff)
			}
		})
	}
}
