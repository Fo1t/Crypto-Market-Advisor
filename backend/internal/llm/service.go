package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
)

const (
	defaultContextSize   = 16384
	minSafetyTokens      = 512
	minimumPayloadTokens = 512
	userSnapshotPrefix   = "Return ru, en, and zh-CN translations for this input.\n\nINPUT SNAPSHOT:\n"
)

// safetyTokens is the reserve kept between the estimated prompt and the real
// context limit. EstimateTokens is tokenizer-independent and has been observed
// to land a few percent below the server's own count, so the reserve scales
// with the window instead of staying a flat 512 tokens.
func safetyTokens(contextSize int) int {
	reserve := contextSize / 16
	if reserve < minSafetyTokens {
		reserve = minSafetyTokens
	}
	return reserve
}

// InferenceStore persists inference traces and serves the backtest cache.
type InferenceStore interface {
	InsertInference(ctx context.Context, rec domain.InferenceRecord) error
	CachedInference(ctx context.Context, cacheKey string) (string, bool, error)
}

// Service turns a feature snapshot into a validated model answer.
type Service struct {
	client *Client
	store  InferenceStore
	log    *slog.Logger
	build  BuildOptions
}

// NewService builds the inference orchestration service.
func NewService(client *Client, store InferenceStore, logger *slog.Logger) *Service {
	return &Service{
		client: client,
		store:  store,
		log:    logging.For(logger, logging.CategoryLLM),
		build:  DefaultBuildOptions(),
	}
}

// Request carries everything one inference needs.
type Request struct {
	Snapshot      domain.FeatureSnapshot
	Validation    ValidationContext
	AnalysisRunID *uuid.UUID
	// BacktestRunID marks the replay an inference belongs to, so the answers of
	// one run can be pulled back out of the trace table as a set.
	BacktestRunID *uuid.UUID
	UseCache      bool
	MaxAllocation float64
}

// Result is the outcome of one inference attempt.
type Result struct {
	Validated   *Validated
	Record      domain.InferenceRecord
	PayloadJSON string
	Trims       []string
}

// Enabled reports whether the LLM is configured to run.
func (s *Service) Enabled() bool { return s.client.Enabled() }

// Client exposes the underlying client for health checks.
func (s *Service) Client() *Client { return s.client }

// Analyze runs one inference, with a single repair retry on invalid output.
// A failed inference is recorded and returned as an error; it never turns into
// a user-visible recommendation.
func (s *Service) Analyze(ctx context.Context, req Request) (*Result, error) {
	cfg := s.client.Config()
	promptVersion := PromptVersionV3
	systemPrompt := SystemPrompt(promptVersion, req.Validation.MinLeverage, req.Validation.MaxLeverage, req.MaxAllocation)
	buildOptions, err := buildOptionsForContext(s.build, cfg, systemPrompt)
	if err != nil {
		return nil, err
	}
	payload, payloadJSON, trims, err := Build(req.Snapshot, buildOptions)
	if err != nil {
		return nil, fmt.Errorf("build payload: %w", err)
	}

	contextSize := cfg.ContextSize
	if contextSize <= 0 {
		contextSize = defaultContextSize
	}
	promptEstimate := EstimateTokens(systemPrompt) + EstimateTokens(userSnapshotPrefix+payloadJSON)
	if promptEstimate+cfg.MaxTokens+safetyTokens(contextSize) > contextSize {
		return nil, fmt.Errorf(
			"LLM request would exceed context: prompt about %d + response %d + reserve %d > context %d",
			promptEstimate, cfg.MaxTokens, safetyTokens(contextSize), contextSize,
		)
	}
	record := domain.InferenceRecord{
		ID:            uuid.New(),
		AnalysisRunID: req.AnalysisRunID,
		BacktestRunID: req.BacktestRunID,
		Symbol:        req.Snapshot.Symbol,
		CreatedAt:     time.Now().UTC(),
		ModelName:     cfg.Model,
		PromptVersion: promptVersion,
		SchemaVersion: ResponseSchemaVersion,
		Input: map[string]any{
			"output_languages": domain.SupportedLanguages(),
			"snapshot":         payload,
		},
	}
	result := &Result{PayloadJSON: payloadJSON, Trims: trims}

	if !s.client.Enabled() {
		record.Status = domain.InferenceDisabled
		record.ErrorMessage = ErrDisabled.Error()
		s.persist(ctx, record)
		result.Record = record
		return result, ErrDisabled
	}

	cacheKey := CacheKey(promptVersion, cfg.Model, systemPrompt+"\x00"+payloadJSON)
	record.CacheKey = &cacheKey

	messages := []Message{
		{Role: RoleSystem, Content: systemPrompt},
		{Role: RoleUser, Content: userSnapshotPrefix + payloadJSON},
	}

	// A cached answer for an identical situation avoids re-running the GPU,
	// which matters most for repeated backtests.
	if req.UseCache && s.store != nil {
		if raw, ok, err := s.store.CachedInference(ctx, cacheKey); err == nil && ok {
			if validated, verr := s.parseAndValidate(raw, req.Validation); verr == nil {
				record.Status = domain.InferenceCached
				record.RawOutput = raw
				record.ParsedOutput = validated
				s.persist(ctx, record)
				result.Validated = validated
				result.Record = record
				return result, nil
			}
		}
	}

	completion, err := s.client.Complete(ctx, messages)
	if err != nil {
		record.Status = statusFor(err)
		record.ErrorMessage = err.Error()
		s.persist(ctx, record)
		result.Record = record
		return result, err
	}
	record.RawOutput = completion.Content
	record.LatencyMS = completion.LatencyMS
	record.PromptTokens = intPtr(completion.Usage.PromptTokens)
	record.CompletionTokens = intPtr(completion.Usage.CompletionTokens)

	validated, verr := s.parseAndValidate(completion.Content, req.Validation)
	if verr == nil {
		record.Status = domain.InferenceOK
		record.ParsedOutput = validated
		s.persist(ctx, record)
		result.Validated = validated
		result.Record = record
		return result, nil
	}

	// One repair attempt, restating the exact validation problems.
	s.log.Info("model answer rejected, attempting repair",
		slog.String("symbol", req.Snapshot.Symbol),
		slog.String("problems", verr.Error()))

	record.RepairAttempted = true
	repairContent := "INVALID OUTPUT:\n" + completion.Content + "\n\n" + RepairPrompt(problemsOf(verr))
	repairMessages := []Message{
		{Role: RoleSystem, Content: systemPrompt},
		{Role: RoleUser, Content: repairContent},
	}
	repairEstimate := EstimateTokens(systemPrompt) + EstimateTokens(repairContent)
	if repairEstimate+cfg.MaxTokens+safetyTokens(contextSize) > contextSize {
		record.Status = domain.InferenceInvalid
		record.ErrorMessage = fmt.Sprintf(
			"repair skipped because it would exceed context: prompt about %d + response %d + reserve %d > context %d (original problems: %v)",
			repairEstimate, cfg.MaxTokens, safetyTokens(contextSize), contextSize, verr,
		)
		s.persist(ctx, record)
		result.Record = record
		return result, fmt.Errorf("%s", record.ErrorMessage)
	}

	repaired, err := s.client.Complete(ctx, repairMessages)
	if err != nil {
		record.Status = statusFor(err)
		record.ErrorMessage = fmt.Sprintf("repair failed: %v (original problems: %v)", err, verr)
		s.persist(ctx, record)
		result.Record = record
		return result, err
	}
	record.RawOutput = repaired.Content
	record.LatencyMS += repaired.LatencyMS

	validated, verr2 := s.parseAndValidate(repaired.Content, req.Validation)
	if verr2 != nil {
		record.Status = domain.InferenceInvalid
		record.ErrorMessage = verr2.Error()
		s.persist(ctx, record)
		result.Record = record
		return result, verr2
	}

	record.Status = domain.InferenceRepaired
	record.ParsedOutput = validated
	s.persist(ctx, record)
	result.Validated = validated
	result.Record = record
	return result, nil
}

func buildOptionsForContext(base BuildOptions, cfg config.LLMConfig, systemPrompt string) (BuildOptions, error) {
	contextSize := cfg.ContextSize
	if contextSize <= 0 {
		contextSize = defaultContextSize
	}
	maxOutput := cfg.MaxTokens
	if maxOutput <= 0 {
		maxOutput = 1800
	}

	available := contextSize - maxOutput - safetyTokens(contextSize) - EstimateTokens(systemPrompt) - EstimateTokens(userSnapshotPrefix)
	if available < minimumPayloadTokens {
		return BuildOptions{}, fmt.Errorf(
			"LLM context %d is too small: only %d tokens remain for the market snapshot after reserving the prompt and %d response tokens",
			contextSize, available, maxOutput,
		)
	}
	// The snapshot budget follows the configured context window. A fixed default
	// would keep trimming the snapshot after the window was enlarged, which both
	// wastes the new room and tells the model its input was reduced.
	base.MaxTokens = available
	if cfg.SnapshotMaxTokens > 0 && cfg.SnapshotMaxTokens < available {
		base.MaxTokens = cfg.SnapshotMaxTokens
	}
	return base, nil
}

func (s *Service) parseAndValidate(raw string, ctx ValidationContext) (*Validated, error) {
	resp, err := ParseResponse(raw)
	if err != nil {
		return nil, &ValidationError{Problems: []string{err.Error()}}
	}
	validated, err := Validate(resp, ctx)
	if err != nil {
		return nil, err
	}
	if problem := multilingualProblem(validated); problem != "" {
		return nil, &ValidationError{Problems: []string{problem}}
	}
	return validated, nil
}

func multilingualProblem(validated *Validated) string {
	if validated == nil {
		return "multilingual recommendation is missing"
	}
	for _, language := range domain.SupportedLanguages() {
		narrative, ok := validated.Translations[language]
		if !ok {
			return fmt.Sprintf("translations.%s is required", language)
		}
		if narrative.Summary == "" || len(narrative.SignalsFor) == 0 ||
			len(narrative.SignalsAgainst) == 0 || len(narrative.Invalidation) == 0 {
			return fmt.Sprintf("translations.%s must include summary, signals_for, signals_against, and invalidation_conditions", language)
		}
		if len(narrative.TakeProfitReasons) != len(validated.TakeProfit) {
			return fmt.Sprintf("translations.%s.take_profit_reasons must have %d items", language, len(validated.TakeProfit))
		}
		if len(narrative.StopLossReasons) != len(validated.StopLoss) {
			return fmt.Sprintf("translations.%s.stop_loss_reasons must have %d items", language, len(validated.StopLoss))
		}
		managementCount := 0
		if validated.Management != nil {
			managementCount = len(validated.Management.Actions)
		}
		if len(narrative.ManagementReasons) != managementCount {
			return fmt.Sprintf("translations.%s.management_reasons must have %d items", language, managementCount)
		}
		if problem := narrativeScriptProblem(language, narrative); problem != "" {
			return problem
		}
	}
	if validated.NewsAssessment != nil {
		for _, language := range domain.SupportedLanguages() {
			reason := validated.NewsAssessment.Reasons[language]
			if problem := narrativeScriptProblem(language, domain.RecommendationNarrative{Summary: reason}); problem != "" {
				return "news_assessment.reasons." + language + ": " + problem
			}
		}
	}
	return ""
}

func narrativeScriptProblem(language string, narrative domain.RecommendationNarrative) string {
	texts := []string{narrative.Summary, narrative.LeverageReason}
	texts = append(texts, narrative.TakeProfitReasons...)
	texts = append(texts, narrative.StopLossReasons...)
	texts = append(texts, narrative.ManagementReasons...)
	texts = append(texts, narrative.SignalsFor...)
	texts = append(texts, narrative.SignalsAgainst...)
	texts = append(texts, narrative.Invalidation...)
	for _, text := range texts {
		if text == "" {
			continue
		}
		latin, cyrillic, han := scriptCounts(text)
		switch language {
		case "ru":
			if cyrillic == 0 && latin > 6 {
				return "every translations.ru text field must be written in Russian"
			}
		case "en":
			if cyrillic > 0 || han > 0 {
				return "every translations.en text field must be written in English"
			}
		case "zh-CN":
			if han == 0 && latin > 6 || cyrillic > 0 {
				return "every translations.zh-CN text field must be written in Simplified Chinese"
			}
		}
	}
	var latin, cyrillic, han int
	for _, text := range texts {
		textLatin, textCyrillic, textHan := scriptCounts(text)
		latin += textLatin
		cyrillic += textCyrillic
		han += textHan
	}
	switch language {
	case "ru":
		if cyrillic == 0 || latin > cyrillic {
			return "translations.ru must contain Russian narrative, not English"
		}
	case "en":
		if latin == 0 || cyrillic > 0 || han > 0 {
			return "translations.en must contain English narrative only"
		}
	case "zh-CN":
		if han == 0 || latin > han*2 || cyrillic > 0 {
			return "translations.zh-CN must contain Simplified Chinese narrative"
		}
	}
	return ""
}

func scriptCounts(text string) (latin, cyrillic, han int) {
	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Cyrillic):
			cyrillic++
		case unicode.In(r, unicode.Han):
			han++
		case unicode.Is(unicode.Latin, r):
			latin++
		}
	}
	return latin, cyrillic, han
}

func (s *Service) persist(ctx context.Context, record domain.InferenceRecord) {
	if s.store == nil {
		return
	}
	// The inference trace must survive even when the caller's context was
	// cancelled mid-request, otherwise failures would leave no evidence.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := s.store.InsertInference(persistCtx, record); err != nil {
		s.log.Warn("store inference failed", slog.String("error", err.Error()))
	}
}

func problemsOf(err error) []string {
	var verr *ValidationError
	if errors.As(err, &verr) {
		return verr.Problems
	}
	return []string{err.Error()}
}

func statusFor(err error) domain.InferenceStatus {
	switch {
	case errors.Is(err, ErrDisabled):
		return domain.InferenceDisabled
	case errors.Is(err, ErrEmptyCompletion):
		return domain.InferenceEmpty
	case errors.Is(err, context.DeadlineExceeded):
		return domain.InferenceTimeout
	default:
		return domain.InferenceTransportError
	}
}

// CacheKey hashes the inputs that fully determine an answer.
func CacheKey(promptVersion, model, payload string) string {
	h := sha256.New()
	h.Write([]byte(promptVersion))
	h.Write([]byte{0})
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}

func intPtr(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}
