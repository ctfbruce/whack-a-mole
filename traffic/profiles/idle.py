"""Sparse background exchanges, with intentionally visible idle periods."""


def next_request(rng, index):
    return rng.uniform(8, 16), rng.randint(64, 256), rng.randint(256, 1024)
