package build

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// owned_content.go — how this plugin's render-prep reaches the content kinds (ops.OpContent; sdk
// deploykit owned_content.go; opencharly/sdk#598). The kit is kind-agnostic: it needs the kind words
// that declare `contents` — a registry fact only the host holds, answered by the host's word-set leg —
// and a way to ask one of them, which is a peer InvokeProvider of kind:<word> ops.OpContent. Both are
// built once per resolve and handed to every render-prep entry point (the root RenderPrepAll and the
// namespace fill), so a namespaced box is asked exactly like a root one.
// validateWordSetsLeg is the host leg that answers the registry word sets (content_kinds among
// them) — the kind charly core registers (validate_project_host.go).
const validateWordSetsLeg = "validate-word-sets"

func ownedContentSource(ctx context.Context, ex *sdk.Executor) (deploykit.OwnedContentSource, error) {
	var words spec.ValidateWordSetsReply
	if err := hostBuildJSON(ctx, ex, validateWordSetsLeg, spec.ValidateWordSetsRequest{}, &words); err != nil {
		return deploykit.OwnedContentSource{}, fmt.Errorf("content kinds: %w", err)
	}
	return deploykit.OwnedContentSource{
		Kinds: words.ContentKinds,
		Ask: func(kind string, req spec.OwnedContentRequest) (*spec.OwnedContentReply, error) {
			params, err := json.Marshal(req)
			if err != nil {
				return nil, err
			}
			res, err := ex.InvokeProvider(ctx, "kind", kind, sdk.OpContent, params, nil, sdk.InvokeProviderOpts{})
			if err != nil {
				return nil, err
			}
			var reply spec.OwnedContentReply
			if err := json.Unmarshal(res, &reply); err != nil {
				return nil, fmt.Errorf("decode kind:%s content reply: %w", kind, err)
			}
			return &reply, nil
		},
	}, nil
}
