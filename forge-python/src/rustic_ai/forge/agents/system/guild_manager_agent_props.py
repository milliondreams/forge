from typing import Optional

from rustic_ai.core.guild.dsl import BaseAgentProps, GuildSpec


class GuildManagerAgentProps(BaseAgentProps):
    """Properties for Forge GuildManagerAgent."""

    guild_spec: GuildSpec
    manager_api_base_url: str
    organization_id: str
    created_by: str
    manager_api_token: Optional[str] = None
