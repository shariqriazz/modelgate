package responses

import (
	. "github.com/shariqriazz/modelgate/internal/constant"
	"github.com/shariqriazz/modelgate/internal/interfaces"
	"github.com/shariqriazz/modelgate/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Claude,
		ConvertOpenAIResponsesRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToOpenAIResponses,
			NonStream: ConvertClaudeResponseToOpenAIResponsesNonStream,
		},
	)
}
