package responses

import (

	. "github.com/shariqriazz/modelgate/internal/translator/gemini-cli/gemini"
	. "github.com/shariqriazz/modelgate/internal/translator/gemini/openai/responses"
)

func ConvertOpenAIResponsesRequestToGeminiCLI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	rawJSON = ConvertOpenAIResponsesRequestToGemini(modelName, rawJSON, stream)
	return ConvertGeminiRequestToGeminiCLI(modelName, rawJSON, stream)
}
