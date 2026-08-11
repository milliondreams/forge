from rustic_ai.core.guild.agent_ext.mixins.health import HeartbeatStatus
from rustic_ai.core.guild.metastore.models import AgentStatus, GuildStatus

from rustic_ai.forge.agents.system.guild_manager_agent import GuildManagerAgent


def test_dynamic_catalog_selector_matches_key_display_name_and_alias():
    profiles = {
        "llm_openai": {
            "display_name": "OpenAI GPT-5.4",
            "aliases": ["gpt-5.4", "gpt"],
        }
    }

    for selector in ("llm_openai", "openai gpt-5.4", "GPT"):
        matches = GuildManagerAgent._match_catalog_profiles(profiles, selector)
        assert matches == [("llm_openai", profiles["llm_openai"])]


def test_dynamic_catalog_selector_reports_ambiguity_to_caller():
    profiles = {
        "first": {"display_name": "First", "aliases": ["gpt"]},
        "second": {"display_name": "Second", "aliases": ["GPT"]},
    }

    assert len(GuildManagerAgent._match_catalog_profiles(profiles, "gpt")) == 2


def test_guild_status_from_health_mapping():
    assert (
        GuildManagerAgent._guild_status_from_health(HeartbeatStatus.OK)
        == GuildStatus.RUNNING
    )
    assert (
        GuildManagerAgent._guild_status_from_health(HeartbeatStatus.WARNING)
        == GuildStatus.WARNING
    )
    assert (
        GuildManagerAgent._guild_status_from_health(HeartbeatStatus.BACKLOGGED)
        == GuildStatus.BACKLOGGED
    )
    assert (
        GuildManagerAgent._guild_status_from_health(HeartbeatStatus.ERROR)
        == GuildStatus.ERROR
    )
    assert (
        GuildManagerAgent._guild_status_from_health(HeartbeatStatus.UNKNOWN)
        == GuildStatus.UNKNOWN
    )


def test_agent_status_from_heartbeat_mapping():
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.OK)
        == AgentStatus.RUNNING
    )
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.WARNING)
        == AgentStatus.RUNNING
    )
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.BACKLOGGED)
        == AgentStatus.RUNNING
    )
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.STARTING)
        == AgentStatus.STARTING
    )
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.PENDING_LAUNCH)
        == AgentStatus.PENDING_LAUNCH
    )
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.ERROR)
        == AgentStatus.ERROR
    )
