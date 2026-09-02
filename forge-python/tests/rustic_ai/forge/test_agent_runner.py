import threading
import time
import json
from unittest.mock import MagicMock, patch

import pytest
from rustic_ai.core.guild.agent import Agent, AgentSpec
from rustic_ai.core.guild.dsl import GuildSpec
from rustic_ai.core.utils.class_utils import get_class_from_name

from rustic_ai.forge.agent_runner import main
from rustic_ai.forge.agent_wrapper import ForgeAgentWrapper


class DummyAgent(Agent):
    def __init__(self, *args, **kwargs):
        pass

    def run(self):
        pass


def fake_get_class(name):
    if "DummyAgent" in name:
        return DummyAgent
    return get_class_from_name(name)


@pytest.fixture
def mock_env(monkeypatch):
    guild_spec = GuildSpec(id="test-guild", name="Test Guild", description="Test")

    monkeypatch.setattr("rustic_ai.core.guild.dsl.get_class_from_name", fake_get_class)

    agent_spec = AgentSpec(
        id="test-agent",
        name="Test Agent",
        description="Test description",
        class_name="DummyAgent",
    )

    monkeypatch.setenv("FORGE_GUILD_JSON", guild_spec.model_dump_json())
    monkeypatch.setenv("FORGE_AGENT_CONFIG_JSON", agent_spec.model_dump_json())
    monkeypatch.setenv("FORGE_CLIENT_TYPE", "InMemoryMessagingBackend")
    monkeypatch.setenv("FORGE_CLIENT_PROPERTIES_JSON", "{}")


def test_agent_wrapper_lifecycle():
    guild_spec = GuildSpec(id="test-guild", name="Test Guild", description="Test")

    with patch(
        "rustic_ai.core.guild.dsl.get_class_from_name", side_effect=fake_get_class
    ):
        agent_spec = AgentSpec(
            id="test-agent",
            name="Test Agent",
            description="Test description",
            class_name="DummyAgent",
        )

    wrapper = ForgeAgentWrapper(
        guild_spec=guild_spec,
        agent_spec=agent_spec,
        messaging_config=MagicMock(),
        machine_id=1,
    )

    wrapper.initialize_agent = MagicMock()

    def delayed_shutdown():
        time.sleep(0.1)
        wrapper.shutdown_event.set()

    thread = threading.Thread(target=delayed_shutdown)
    thread.start()

    wrapper.run()

    thread.join()

    wrapper.initialize_agent.assert_called_once()
    assert wrapper.is_running is False


def test_agent_runner_main(mock_env):
    with patch("rustic_ai.forge.agent_runner.ForgeAgentWrapper") as MockWrapper:
        mock_instance = MockWrapper.return_value

        with patch("sys.exit") as mock_exit:
            main()

            MockWrapper.assert_called_once()
            mock_instance.run.assert_called_once()
            mock_exit.assert_called_once_with(0)


def test_agent_runner_missing_env(monkeypatch):
    monkeypatch.delenv("FORGE_GUILD_JSON", raising=False)

    with patch("sys.exit") as mock_exit:
        main()
        mock_exit.assert_called_once_with(1)


def test_agent_runner_lenient_guild_spec_fallback(monkeypatch):
    bad_guild_json = json.dumps(
        {
            "id": "test-guild",
            "name": "Test Guild",
            "description": "Test",
            "agents": [
                {
                    "id": "missing-agent",
                    "name": "Missing Agent",
                    "description": "Missing class should not crash startup",
                    "class_name": "rustic_ai.missing.agent.DoesNotExist",
                }
            ],
        }
    )

    monkeypatch.setenv("FORGE_GUILD_JSON", bad_guild_json)
    monkeypatch.setenv(
        "FORGE_AGENT_CONFIG_JSON",
        json.dumps(
            {
                "id": "test-agent",
                "name": "Test Agent",
                "description": "Test description",
                "class_name": "DummyAgent",
            }
        ),
    )
    monkeypatch.setenv("FORGE_CLIENT_TYPE", "InMemoryMessagingBackend")
    monkeypatch.setenv("FORGE_CLIENT_PROPERTIES_JSON", "{}")
    monkeypatch.setattr("rustic_ai.core.guild.dsl.get_class_from_name", fake_get_class)

    with patch("rustic_ai.forge.agent_runner.ForgeAgentWrapper") as MockWrapper:
        mock_instance = MockWrapper.return_value
        with patch("sys.exit") as mock_exit:
            main()
            MockWrapper.assert_called_once()
            mock_instance.run.assert_called_once()
            mock_exit.assert_called_once_with(0)


def test_agent_runner_lenient_agent_spec_with_guild_spec_props(monkeypatch):
    guild_json = json.dumps(
        {
            "id": "test-guild",
            "name": "Test Guild",
            "description": "Test",
            "agents": [
                {
                    "id": "missing-agent",
                    "name": "Missing Agent",
                    "description": "Existing class in guild_spec props",
                    "class_name": "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
                }
            ],
        }
    )
    manager_agent_json = json.dumps(
        {
            "id": "manager-agent",
            "name": "Guild Manager",
            "description": "Manager",
            "class_name": "rustic_ai.forge.agents.system.guild_manager_agent.GuildManagerAgent",
            "properties": {
                "guild_spec": json.loads(guild_json),
                "manager_api_base_url": "http://127.0.0.1:9090",
                "organization_id": "org-1",
                "created_by": "user-1",
            },
        }
    )

    monkeypatch.setenv("FORGE_GUILD_JSON", guild_json)
    monkeypatch.setenv("FORGE_AGENT_CONFIG_JSON", manager_agent_json)
    monkeypatch.setenv("FORGE_CLIENT_TYPE", "InMemoryMessagingBackend")
    monkeypatch.setenv("FORGE_CLIENT_PROPERTIES_JSON", "{}")

    with patch("rustic_ai.forge.agent_runner.ForgeAgentWrapper") as MockWrapper:
        mock_instance = MockWrapper.return_value
        with patch("sys.exit") as mock_exit:
            main()
            MockWrapper.assert_called_once()
            mock_instance.run.assert_called_once()
            mock_exit.assert_called_once_with(0)
