package gemini

import (
	. "github.com/shariqriazz/modelgate/internal/constant"
	"github.com/shariqriazz/modelgate/internal/interfaces"
	"github.com/shariqriazz/modelgate/internal/translator/translator"
)

func init() {
	translator.Register(
		Gemini,
		Antigravity,
		ConvertGeminiRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:     ConvertAntigravityResponseToGemini,
			NonStream:  ConvertAntigravityResponseToGeminiNonStream,
			TokenCount: GeminiTokenCount,
		},
	)
}
