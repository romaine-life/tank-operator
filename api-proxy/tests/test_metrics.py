"""Unit tests for the api-proxy upstream-status metric classification.

metrics.py has no proto/grpc dependencies (only aiohttp + prometheus_client),
so this test imports it directly. The src bootstrap mirrors how CI makes the
package importable (editable install); inserting src first keeps a local
`pytest` run working without an install step.
"""
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from tank_api_proxy import metrics as proxy_metrics  # noqa: E402


def _count(counter) -> float:
    return counter.labels(provider=proxy_metrics.PROVIDER)._value.get()


class RecordUpstreamStatusTest(unittest.TestCase):
    def test_429_increments_rate_limit_and_4xx_bucket(self) -> None:
        before_429 = _count(proxy_metrics.upstream_429_total)
        before_4xx = proxy_metrics.upstream_status_total.labels(
            provider=proxy_metrics.PROVIDER, status_class="4xx"
        )._value.get()
        before_401 = _count(proxy_metrics.upstream_401_total)

        proxy_metrics.record_upstream_status(429)

        self.assertEqual(_count(proxy_metrics.upstream_429_total), before_429 + 1)
        self.assertEqual(
            proxy_metrics.upstream_status_total.labels(
                provider=proxy_metrics.PROVIDER, status_class="4xx"
            )._value.get(),
            before_4xx + 1,
        )
        # 429 is rate-limit, not auth: the 401 counter must not move.
        self.assertEqual(_count(proxy_metrics.upstream_401_total), before_401)

    def test_401_does_not_increment_429(self) -> None:
        before_429 = _count(proxy_metrics.upstream_429_total)
        before_401 = _count(proxy_metrics.upstream_401_total)

        proxy_metrics.record_upstream_status(401)

        self.assertEqual(_count(proxy_metrics.upstream_401_total), before_401 + 1)
        self.assertEqual(_count(proxy_metrics.upstream_429_total), before_429)

    def test_200_increments_neither_signature_counter(self) -> None:
        before_429 = _count(proxy_metrics.upstream_429_total)
        before_401 = _count(proxy_metrics.upstream_401_total)

        proxy_metrics.record_upstream_status(200)

        self.assertEqual(_count(proxy_metrics.upstream_429_total), before_429)
        self.assertEqual(_count(proxy_metrics.upstream_401_total), before_401)

    def test_none_status_is_noop(self) -> None:
        before_429 = _count(proxy_metrics.upstream_429_total)
        proxy_metrics.record_upstream_status(None)
        self.assertEqual(_count(proxy_metrics.upstream_429_total), before_429)


class RecordEnvoySdsStatsTest(unittest.TestCase):
    def test_reexports_bounded_sds_counters_from_envoy_admin_text(self) -> None:
        proxy_metrics.record_envoy_sds_stats(
            "\n".join(
                [
                    "listener.0.0.0.0_8443.server_ssl_socket_factory.ssl_context_update_by_sds: 2",
                    "listener.127.0.0.1_8443.server_ssl_socket_factory.ssl_context_update_by_sds: 1",
                    "sds.api_proxy_leaf.key_rotation_failed: 3",
                    "sds.other_secret.key_rotation_failed: not-a-number",
                ]
            )
        )

        self.assertEqual(
            proxy_metrics.envoy_sds_ssl_context_updates.labels(
                provider=proxy_metrics.PROVIDER
            )._value.get(),
            3,
        )
        self.assertEqual(
            proxy_metrics.envoy_sds_key_rotation_failed.labels(
                provider=proxy_metrics.PROVIDER,
                secret="api_proxy_leaf",
            )._value.get(),
            3,
        )


if __name__ == "__main__":
    unittest.main()
