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

    # ── 鉴权（JWT，可选；默认关闭以保持 demo 零配置）──
    jwt_secret: str = Field(
        default="",
        description=(
            "JWT 签名密钥。留空 = **关闭鉴权**（demo/CI 默认，行为与之前完全一致）；"
            "设为非空字符串则对 /api/v1/games* 启用 Bearer Token，且座位绑定"
            "（只能操作自己座位的对局，查询只返回自己的手牌）"
        ),
    )
    jwt_algorithm: str = Field(default="HS256", description="JWT 签名算法（HS256 足够 demo，无需额外加密库）")
    jwt_expire_minutes: int = Field(default=720, ge=1, description="Token 有效期（分钟），默认 12 小时")

    # ── 缓存（cache-aside，可选）──
    cache_redis_url: str = Field(
        default="",
        description=(
            "**缓存用** Redis 连接串（与 REDIS_URL 的「存储」角色区分开）。非空且 cache_ttl_seconds>0 时，"
            "对局读走缓存、写后删缓存。留空 = 不开缓存；注意 REDIS_URL 是「Redis 当存储」，两者不要混用"
        ),
    )
    cache_ttl_seconds: int = Field(
        default=30,
        ge=0,
        description=(
            "对局读缓存 TTL（秒）。0 = 关闭缓存；仅当 cache_redis_url 非空时生效："
            "读走 Redis 缓存、写（打牌）后删缓存，是 cache-aside 的最小实现"
        ),
    )

    @property
    def cors_origin_list(self) -> list[str]:
        """把逗号分隔的 ``cors_origins`` 解析成中间件需要的列表。"""
        origins = [origin.strip() for origin in self.cors_origins.split(",")]
        return [origin for origin in origins if origin]
