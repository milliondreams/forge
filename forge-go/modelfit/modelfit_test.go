package modelfit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadProfilesValidatesAndNormalizesCatalog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dependencyConfigPath := filepath.Join(dir, "agent-dependencies.yaml")
	catalogPath := filepath.Join(dir, "local-model-catalog.yaml")

	require.NoError(t, os.WriteFile(dependencyConfigPath, []byte(`
llm_local_small:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.llm.LLM
  properties:
    model: openai/rustic/small
    conf:
      base_url: http://10.0.2.101:55262/v1
llm_local_large:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.llm.LLM
  properties:
    model: openai/rustic/large
    base_url: http://localhost:55262/v1
`), 0o644))
	require.NoError(t, os.WriteFile(catalogPath, []byte(`
models:
  - id: large
    display_name: Large
    dependency_key: llm_local_large
    model_name: openai/rustic/large
    parameter_count_b: 7
    quantization: Q4_K_M
    context_length: 16384
    min_ram_bytes: 2147483648
    preferred_vram_bytes: 4294967296
    estimated_memory_bytes: 4294967296
    use_case_tags: [Coding, coding, General]
    quality_rank: 1
  - id: small
    display_name: Small
    dependency_key: llm_local_small
    model_name: openai/rustic/small
    parameter_count_b: 2
    quantization: Q4_K_M
    context_length: 8192
    min_ram_bytes: 1073741824
    preferred_vram_bytes: 2147483648
    estimated_memory_bytes: 2147483648
    use_case_tags: [general, chat]
    quality_rank: 3
`), 0o644))

	profiles, err := LoadProfiles(catalogPath, dependencyConfigPath)
	require.NoError(t, err)
	require.Len(t, profiles, 2)
	require.Equal(t, "llm_local_large", profiles[0].DependencyKey)
	require.Equal(t, []string{"coding", "general"}, profiles[0].UseCaseTags)
	require.Equal(t, "rustic_ai.core.llm.LLM", profiles[0].ProvidedType)
	require.Equal(t, "http://localhost:55262/v1", profiles[0].BaseURL)
	require.Equal(t, "llm_local_small", profiles[1].DependencyKey)
	require.Equal(t, "http://10.0.2.101:55262/v1", profiles[1].BaseURL)
	require.Equal(t, []string{"chat", "general"}, profiles[1].UseCaseTags)
}

func TestLoadProfilesRejectsMissingDependencyMapping(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dependencyConfigPath := filepath.Join(dir, "agent-dependencies.yaml")
	catalogPath := filepath.Join(dir, "local-model-catalog.yaml")

	require.NoError(t, os.WriteFile(dependencyConfigPath, []byte(`
llm_local_small:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.llm.LLM
  properties:
    model: openai/rustic/small
`), 0o644))
	require.NoError(t, os.WriteFile(catalogPath, []byte(`
models:
  - id: missing
    display_name: Missing
    dependency_key: llm_local_missing
    model_name: openai/rustic/missing
    parameter_count_b: 1
    quantization: Q4_K_M
    context_length: 4096
    min_ram_bytes: 1073741824
    preferred_vram_bytes: 1073741824
    estimated_memory_bytes: 1073741824
    quality_rank: 1
`), 0o644))

	_, err := LoadProfiles(catalogPath, dependencyConfigPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), `dependency key "llm_local_missing" not found`)
}

func TestLoadProfilesUsesAgentOSLocalModelEndpointOverride(t *testing.T) {
	dir := t.TempDir()
	dependencyConfigPath := filepath.Join(dir, "agent-dependencies.yaml")
	catalogPath := filepath.Join(dir, "local-model-catalog.yaml")
	t.Setenv("AGENTOS_LOCAL_MODEL_BASE_URL", "http://10.0.2.101:55262/v1")

	require.NoError(t, os.WriteFile(dependencyConfigPath, []byte(`
llm_local_small:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.llm.LLM
  properties:
    model: openai/rustic/small
    conf:
      base_url: http://localhost:55262/v1
`), 0o644))
	require.NoError(t, os.WriteFile(catalogPath, []byte(`
models:
  - id: small
    display_name: Small
    dependency_key: llm_local_small
    model_name: openai/rustic/small
    parameter_count_b: 2
    quantization: Q4_K_M
    context_length: 8192
    min_ram_bytes: 1073741824
    estimated_memory_bytes: 2147483648
`), 0o644))

	profiles, err := LoadProfiles(catalogPath, dependencyConfigPath)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	require.Equal(t, "http://10.0.2.101:55262/v1", profiles[0].BaseURL)
}

func TestRecommendRanksAndFiltersLocalModels(t *testing.T) {
	t.Parallel()

	profiles := []ModelProfile{
		{
			ID:                   "small",
			DisplayName:          "Small",
			DependencyKey:        "llm_local_small",
			ModelName:            "openai/rustic/small",
			ParameterCountB:      2,
			ContextLength:        8192,
			MinRAMBytes:          2 * 1024 * 1024 * 1024,
			EstimatedMemoryBytes: 2 * 1024 * 1024 * 1024,
			UseCaseTags:          []string{"general", "coding"},
			QualityRank:          3,
			TokenSpeedHint:       40,
		},
		{
			ID:                   "large",
			DisplayName:          "Large",
			DependencyKey:        "llm_local_large",
			ModelName:            "openai/rustic/large",
			ParameterCountB:      7,
			ContextLength:        16384,
			MinRAMBytes:          10 * 1024 * 1024 * 1024,
			EstimatedMemoryBytes: 10 * 1024 * 1024 * 1024,
			PreferredVRAMBytes:   8 * 1024 * 1024 * 1024,
			PreferredDiscreteGPU: true,
			UseCaseTags:          []string{"coding"},
			QualityRank:          1,
			TokenSpeedHint:       20,
		},
		{
			ID:                   "embed",
			DisplayName:          "Embed",
			DependencyKey:        "llm_local_embed",
			ModelName:            "openai/rustic/embed",
			ParameterCountB:      1,
			ContextLength:        8192,
			MinRAMBytes:          1 * 1024 * 1024 * 1024,
			EstimatedMemoryBytes: 1 * 1024 * 1024 * 1024,
			EmbeddingOnly:        true,
			UseCaseTags:          []string{"embedding"},
			QualityRank:          2,
		},
	}
	system := SystemProfile{
		AvailableRAMBytes: 8 * 1024 * 1024 * 1024,
		TotalRAMBytes:     16 * 1024 * 1024 * 1024,
		CPUCores:          8,
	}

	results := Recommend(profiles, system, QueryOptions{
		UseCase:      "coding",
		RunnableOnly: true,
	})
	require.Len(t, results, 1)
	require.Equal(t, "llm_local_small", results[0].DependencyKey)
	require.Equal(t, FitPerfect, results[0].FitLevel)

	all := Recommend(profiles, system, QueryOptions{})
	require.Len(t, all, 3)
	require.Equal(t, "llm_local_embed", all[0].DependencyKey)
	require.Equal(t, "llm_local_small", all[1].DependencyKey)
	require.Equal(t, "llm_local_large", all[2].DependencyKey)
	require.False(t, all[2].Runnable)
	require.Equal(t, FitTooTight, all[2].FitLevel)
	require.Contains(t, all[2].Explanations, "Discrete GPU preferred, but no runtime-usable accelerator detected")
}

func TestRecommendUsesRuntimeUsableAcceleratorAndDiagnostics(t *testing.T) {
	t.Parallel()

	profiles := []ModelProfile{
		{
			ID:                   "large",
			DisplayName:          "Large",
			DependencyKey:        "llm_local_large",
			ModelName:            "openai/rustic/large",
			ParameterCountB:      7,
			ContextLength:        16384,
			MinRAMBytes:          8 * 1024 * 1024 * 1024,
			EstimatedMemoryBytes: 8 * 1024 * 1024 * 1024,
			PreferredVRAMBytes:   6 * 1024 * 1024 * 1024,
			PreferredDiscreteGPU: true,
			QualityRank:          1,
			TokenSpeedHint:       20,
		},
	}
	system := SystemProfile{
		TotalRAMBytes:             16 * 1024 * 1024 * 1024,
		AvailableRAMBytes:         8 * 1024 * 1024 * 1024,
		CPUCores:                  8,
		HasGPU:                    true,
		GPUCount:                  1,
		Backend:                   BackendCUDA,
		RuntimeUsableAcceleration: true,
		SelectedAcceleratorID:     "nvidia-0",
		Confidence:                DetectionConfidenceProbe,
		ReasonCodes:               []DiagnosticReason{ReasonRuntimeDeviceDetected},
		Runtime: RuntimeCapabilityProfile{
			RuntimeAvailable: true,
			SelectedBackend:  BackendCUDA,
			Confidence:       DetectionConfidenceProbe,
			UsableAccelerators: []UsableAccelerator{
				{
					ID:               "nvidia-0",
					Vendor:           "nvidia",
					Name:             "RTX",
					Backend:          BackendCUDA,
					TotalMemoryBytes: 12 * 1024 * 1024 * 1024,
					Discrete:         true,
				},
			},
		},
	}

	results := Recommend(profiles, system, QueryOptions{})
	require.Len(t, results, 1)
	require.Equal(t, BackendCUDA, results[0].SelectedBackend)
	require.Equal(t, "nvidia-0", results[0].SelectedAcceleratorID)
	require.True(t, results[0].RuntimeUsableAcceleration)
	require.Equal(t, uint64(12*1024*1024*1024), results[0].AvailableMemoryBytes)
	require.Contains(t, results[0].Explanations, "Using runtime-usable accelerator memory pool")
	require.Contains(t, results[0].Explanations, "Runtime probe detected accelerator devices")
}

func TestRecommendTreatsHybridNVIDIAWithoutRuntimeAsCPUOnly(t *testing.T) {
	t.Parallel()

	profiles := []ModelProfile{
		{
			ID:                   "large",
			DisplayName:          "Large",
			DependencyKey:        "llm_local_large",
			ModelName:            "openai/rustic/large",
			ParameterCountB:      7,
			ContextLength:        16384,
			MinRAMBytes:          8 * 1024 * 1024 * 1024,
			EstimatedMemoryBytes: 8 * 1024 * 1024 * 1024,
			PreferredVRAMBytes:   6 * 1024 * 1024 * 1024,
			PreferredDiscreteGPU: true,
			QualityRank:          1,
		},
	}
	system := SystemProfile{
		TotalRAMBytes:             16 * 1024 * 1024 * 1024,
		AvailableRAMBytes:         8 * 1024 * 1024 * 1024,
		CPUCores:                  8,
		HasGPU:                    true,
		GPUCount:                  2,
		Backend:                   BackendCPU,
		RuntimeUsableAcceleration: false,
		Confidence:                DetectionConfidenceHeuristic,
		ReasonCodes: []DiagnosticReason{
			ReasonHybridGPUPresentOffload,
			ReasonNVIDIAPresentRuntimeCPUOnly,
		},
	}

	results := Recommend(profiles, system, QueryOptions{})
	require.Len(t, results, 1)
	require.Equal(t, BackendCPU, results[0].SelectedBackend)
	require.False(t, results[0].RuntimeUsableAcceleration)
	require.Contains(t, results[0].Explanations, "NVIDIA GPU detected, but runtime is currently CPU-only")
	require.Contains(t, results[0].Explanations, "Hybrid GPU setup detected, but offload runtime is not currently usable")
	require.Contains(t, results[0].Explanations, "Discrete GPU preferred, but no runtime-usable accelerator detected")
}

func TestClassifyBoundaries(t *testing.T) {
	t.Parallel()

	require.Equal(t, FitPerfect, classify(70, true))
	require.Equal(t, FitGood, classify(70.01, true))
	require.Equal(t, FitGood, classify(85, true))
	require.Equal(t, FitMarginal, classify(85.01, true))
	require.Equal(t, FitMarginal, classify(100, true))
	require.Equal(t, FitTooTight, classify(100.01, true))
	require.Equal(t, FitTooTight, classify(0, false))
}
