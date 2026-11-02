"""应用配置：基于 pydantic-settings，支持环境变量与 ``.env`` 文件。

环境变量名与字段名一一对应（大小写不敏感），例如：

- ``APP_NAME``
- ``VERSION``
- ``CORS_ORIGINS``（逗号分隔，例如 ``http://localhost:5173,https://demo.example.com``）
- ``MAX_GAMES``
- ``GAME_TTL_SECONDS``
- ``DATABASE_URL``（为空 = 内存模式；例如 ``sqlite+aiosqlite:///./games.db``
  或生产 ``mysql+aiomysql://user:pass@host:3306/mahjong``）
- ``DB_ECHO``（打印 SQL，排查问题时开）
- ``REDIS_URL``（非空则切换为 Redis 对局仓库，优先级高于 ``DATABASE_URL``；
  例如 ``redis://127.0.0.1:6379/0``；留空 = 不启用 Redis）
"""

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """服务运行配置。

    ``cors_origins`` 故意用字符串而不是 ``list[str]``：
    pydantic-settings 对复杂类型要求环境变量是 JSON（``["a","b"]``），
    而运维习惯写逗号分隔，这里统一在 :attr:`cors_origin_list` 里解析，
    避免部署时因为引号 / 空格踩坑。
    """

    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        case_sensitive=False,
        extra="ignore",
    )

    app_name: str = Field(default="广东麻将（推倒胡）后端服务", description="服务名称")
    version: str = Field(default="0.1.0", description="服务版本号")
    cors_origins: str = Field(
        default="*",
        description="允许跨域来源，逗号分隔；demo 用 *，生产必须收紧为具体域名",
    )
    max_games: int = Field(default=100, ge=1, description="内存中同时保留的对局数上限")
    game_ttl_seconds: int = Field(default=7200, ge=1, description="对局闲置超过多少秒后自动清理")

    database_url: str = Field(
        default="",
        description=(
            "数据库连接串（SQLAlchemy 异步 DSN）。留空 = 纯内存模式（零依赖，demo/CI 默认）；"
            "例如 sqlite+aiosqlite:///./games.db（本地验证）"
            "或 mysql+aiomysql://user:pass@127.0.0.1:3306/mahjong（生产）"
        ),
    )
    redis_url: str = Field(
        default="",
        description=(
            "Redis 连接串（redis://[:password@]host:port/db）。非空则切换为 Redis 对局仓库，"
            "优先级高于 database_url；留空 = 不启用 Redis（默认内存或 SQL 分支）。"
            "对局的闲置 TTL 复用 game_ttl_seconds，不再单独设置缓存 TTL，避免两套过期时间打架"
        ),
    )
    db_echo: bool = Field(default=False, description="是否打印 SQLAlchemy 生成的 SQL（调试用）")

    @property
    def cors_origin_list(self) -> list[str]:
        """把逗号分隔的 ``cors_origins`` 解析成中间件需要的列表。"""
        origins = [origin.strip() for origin in self.cors_origins.split(",")]
        return [origin for origin in origins if origin]
