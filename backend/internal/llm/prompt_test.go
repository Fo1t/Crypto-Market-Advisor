package llm

import (
	"strings"
	"testing"
)

func TestSystemPromptRequiresThreeTranslationsWithoutChangingMachineSchema(t *testing.T) {
	prompt := SystemPrompt(PromptVersionV2, 5, 50, 15)
	for _, language := range []string{`"ru"`, `"en"`, `"zh-CN"`} {
		if !strings.Contains(prompt, language) {
			t.Fatalf("language key %s is missing", language)
		}
	}
	if !strings.Contains(prompt, `"action": "OPEN_LONG" | "OPEN_SHORT"`) {
		t.Fatal("prompt changed the stable machine schema")
	}
	if !strings.Contains(prompt, "All three translation objects") {
		t.Fatal("prompt must make all three translations mandatory")
	}
}

func TestMultilingualValidation(t *testing.T) {
	validated := mustValidate(t, validMultilingual, baseContext())
	if problem := multilingualProblem(validated); problem != "" {
		t.Fatalf("expected three valid translations, got %q", problem)
	}

	delete(validated.Translations, "zh-CN")
	if problem := multilingualProblem(validated); !strings.Contains(problem, "translations.zh-CN is required") {
		t.Fatalf("expected missing Chinese translation rejection, got %q", problem)
	}

	validated = mustValidate(t, validMultilingual, baseContext())
	russian := validated.Translations["ru"]
	russian.Summary = "This summary is accidentally English."
	validated.Translations["ru"] = russian
	if problem := multilingualProblem(validated); !strings.Contains(problem, "translations.ru") {
		t.Fatalf("expected wrong Russian script rejection, got %q", problem)
	}
}
