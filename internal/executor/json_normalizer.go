package executor

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/JackDrogon/Cogito/internal/provider"
)

func JSONOutputNormalizer() ResultNormalizer {
	base := DefaultNormalizer()

	return ResultNormalizerFunc(func(ctx context.Context, input NormalizerInput) (*provider.StepResult, error) {
		result, err := base.Normalize(ctx, input)
		if err != nil {
			return nil, err
		}

		payload := strings.TrimSpace(string(input.Stdout))
		if payload == "" {
			return nil, newError(ErrorCodeResult, "malformed structured output: empty stdout")
		}

		var raw json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return nil, wrapError(ErrorCodeResult, "malformed structured output", err)
		}

		result.StructuredOutput = raw

		return result, nil
	})
}
