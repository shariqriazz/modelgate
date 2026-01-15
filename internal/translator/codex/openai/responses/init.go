package responses

import (
	. "github.com/shariqriazz/modelgate/internal/constant"
	"github.com/shariqriazz/modelgate/internal/interfaces"
	"github.com/shariqriazz/modelgate/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Codex,
		ConvertOpenAIResponsesRequestToCodex,
		interfaces.TranslateResponse{
			Stream:    ConvertCodexResponseToOpenAIResponses,
			NonStream: ConvertCodexResponseToOpenAIResponsesNonStream,
		},
	)
}
