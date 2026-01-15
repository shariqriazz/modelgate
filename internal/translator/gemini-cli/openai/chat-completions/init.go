package chat_completions

import (
	. "github.com/shariqriazz/modelgate/internal/constant"
	"github.com/shariqriazz/modelgate/internal/interfaces"
	"github.com/shariqriazz/modelgate/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		GeminiCLI,
		ConvertOpenAIRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:    ConvertCliResponseToOpenAI,
			NonStream: ConvertCliResponseToOpenAINonStream,
		},
	)
}
