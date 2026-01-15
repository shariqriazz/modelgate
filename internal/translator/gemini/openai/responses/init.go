package responses

import (
	. "github.com/shariqriazz/modelgate/internal/constant"
	"github.com/shariqriazz/modelgate/internal/interfaces"
	"github.com/shariqriazz/modelgate/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Gemini,
		ConvertOpenAIResponsesRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    ConvertGeminiResponseToOpenAIResponses,
			NonStream: ConvertGeminiResponseToOpenAIResponsesNonStream,
		},
	)
}
