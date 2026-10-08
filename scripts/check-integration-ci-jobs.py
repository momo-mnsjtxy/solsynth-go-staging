#!/usr/bin/env python3
"""Read saved GitHub run/all-job pages; never queries or changes external state."""
import argparse
import json
from pathlib import Path
import sys

EXPECTED_STEPS = {
    'checks': {'Ordinary build, vet and race checks'},
    'PostgreSQL group-a': {'Require assigned actual PostgreSQL cases and restart persistence', "Preserve this group's result evidence"},
    'PostgreSQL group-b': {'Require assigned actual PostgreSQL cases and restart persistence', "Preserve this group's result evidence"},
    'contracts': {'Require both groups and ordinary checks to succeed', 'Recompute the complete fail-closed contract result'},
}


def flatten(pages):
    if isinstance(pages, dict):
        pages = [pages]
    if not isinstance(pages, list) or not pages or any(not isinstance(p, dict) or not isinstance(p.get('jobs'), list) for p in pages):
        raise ValueError('all paginated workflow job objects are required')
    totals = {p.get('total_count') for p in pages}
    jobs = [job for page in pages for job in page['jobs']]
    if len(totals) != 1 or totals != {len(jobs)} or any(not isinstance(job, dict) for job in jobs):
        raise ValueError('incomplete or inconsistent workflow-job pagination')
    ids = [job.get('id') for job in jobs]
    if any(type(v) is not int for v in ids) or len(ids) != len(set(ids)):
        raise ValueError('duplicate or invalid workflow job IDs')
    return jobs


def check(run, pages, commit, run_id, attempt):
    if run.get('id') != run_id or run.get('run_attempt') != attempt or run.get('head_sha') != commit:
        raise ValueError('run does not match the expected commit, ID and attempt')
    jobs = flatten(pages)
    names = [job.get('name') for job in jobs]
    if len(names) != len(set(names)) or set(names) != set(EXPECTED_STEPS):
        raise ValueError('both groups, ordinary checks and aggregate must each occur exactly once')
    for job in jobs:
        if job.get('run_id') != run_id or job.get('head_sha') != commit or job.get('run_attempt', attempt) != attempt:
            raise ValueError('foreign or stale job: ' + job['name'])
        if job.get('status') != 'completed' or job.get('conclusion') != 'success':
            raise ValueError('unfinished or unsuccessful job: ' + job['name'])
        steps = job.get('steps', [])
        for name in EXPECTED_STEPS[job['name']]:
            found = [step for step in steps if step.get('name') == name]
            if len(found) != 1 or found[0].get('status') != 'completed' or found[0].get('conclusion') != 'success':
                raise ValueError('required step missing, skipped or unsuccessful: ' + job['name'] + ' / ' + name)
    if run.get('status') != 'completed' or run.get('conclusion') != 'success':
        raise ValueError('workflow is not completed successfully')
    return jobs


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('run_json', type=Path)
    parser.add_argument('jobs_json', type=Path)
    parser.add_argument('--commit', required=True)
    parser.add_argument('--run-id', type=int, required=True)
    parser.add_argument('--attempt', type=int, required=True)
    args = parser.parse_args()
    try:
        run = json.loads(args.run_json.read_text())
        pages = json.loads(args.jobs_json.read_text())
        jobs = flatten(pages)
        for job in sorted(jobs, key=lambda v: v.get('name', '')):
            print(f"{job.get('name')}: {job.get('status')} / {job.get('conclusion')} (job {job.get('id')})")
        check(run, pages, args.commit, args.run_id, args.attempt)
        print('Every expected job and required step passed for the exact commit/run/attempt. Recompute downloaded group artifacts separately for case-level acceptance.')
    except (ValueError, TypeError, KeyError, OSError) as error:
        sys.exit(str(error))


if __name__ == '__main__':
    main()
