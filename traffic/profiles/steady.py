"""Paced padding with small timing jitter."""


def next_request(rng, index):
    return rng.uniform(0.8, 1.2), 1024, 16384
