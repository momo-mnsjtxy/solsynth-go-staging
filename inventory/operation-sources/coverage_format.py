#!/usr/bin/env python3
"""Lossless bounded-file encoding of the generated operation plan.

Reassembly reproduces the original canonical UTF-8 JSON byte stream, including
all operation records and their ordering. No backend contract data is dropped.
"""
import argparse
import hashlib
import itertools
import json
import re
from pathlib import Path

FORMAT='lossless-operation-plan-parts-v1'
MAX_PART_BYTES=45000


def canonical(value):
    return (json.dumps(value,ensure_ascii=False,indent=2)+'\n').encode()


def compact(value):
    return (json.dumps(value,ensure_ascii=False,separators=(',',':'))+'\n').encode()


def write_plan(path,plan):
    path=Path(path)
    directory=path.with_name(path.stem+'-parts')
    directory.mkdir(parents=True,exist_ok=True)
    original=canonical(plan)
    index={'format':FORMAT,'canonical_sha256':hashlib.sha256(original).hexdigest(),
           'canonical_git_blob':hashlib.sha1(b'blob '+str(len(original)).encode()+b'\0'+original).hexdigest(),
           'canonical_bytes':len(original),'measurement':plan['measurement'],
           'reassemble':'python3 inventory/operation-sources/coverage_format.py --assemble docs/operation-coverage-plan.json --output /tmp/operation-coverage-plan.json',
           'parts_directory':directory.name,'fields':[]}
    def save(name,value):
        data=compact(value)
        if len(data)>MAX_PART_BYTES:
            raise ValueError('part exceeds bounded upload size: '+name)
        (directory/name).write_bytes(data)
        return {'file':name,'bytes':len(data),'sha256':hashlib.sha256(data).hexdigest()}
    for key,value in plan.items():
        field={'key':key,'kind':'list' if isinstance(value,list) else 'value','parts':[]}
        if not isinstance(value,list):
            field['parts'].append(save(key+'.json',value))
        else:
            groups=[('',value)]
            if key=='http_operations':
                groups=[(Path(source).stem,list(rows)) for source,rows in itertools.groupby(value,key=lambda x:x['source_path'])]
            serial=0
            for label,rows in groups:
                chunks=[];chunk=[]
                for row in rows:
                    if chunk and len(compact(chunk+[row]))>MAX_PART_BYTES:
                        chunks.append(chunk);chunk=[]
                    chunk.append(row)
                if chunk:chunks.append(chunk)
                for chunk in chunks:
                    serial+=1
                    suffix='-'+re.sub('[^A-Za-z0-9_-]','_',label) if label else ''
                    field['parts'].append(save(f'{key}-{serial:04d}{suffix}.json',chunk))
        index['fields'].append(field)
    path.write_bytes(canonical(index))
    assert read_plan(path)==plan
    return index


def read_plan(path):
    path=Path(path);index=json.loads(path.read_bytes())
    if index.get('format')!=FORMAT:return index
    name=index['parts_directory']
    if Path(name).name!=name:raise ValueError('invalid parts directory')
    directory=path.parent/name
    plan={}
    for field in index['fields']:
        values=[]
        for part in field['parts']:
            name=part['file']
            if Path(name).name!=name:raise ValueError('invalid part name')
            data=(directory/name).read_bytes()
            if len(data)!=part['bytes'] or hashlib.sha256(data).hexdigest()!=part['sha256']:
                raise ValueError('coverage part integrity mismatch: '+name)
            value=json.loads(data)
            if field['kind']=='list':
                if not isinstance(value,list):raise ValueError('part shape mismatch')
                values.extend(value)
            elif field['kind']=='value':values.append(value)
            else:raise ValueError('unknown field kind')
        if field['key'] in plan:raise ValueError('duplicate root field')
        if field['kind']=='value' and len(values)!=1:raise ValueError('root value count')
        plan[field['key']]=values if field['kind']=='list' else values[0]
    data=canonical(plan)
    blob=hashlib.sha1(b'blob '+str(len(data)).encode()+b'\0'+data).hexdigest()
    if len(data)!=index['canonical_bytes'] or hashlib.sha256(data).hexdigest()!=index['canonical_sha256'] or blob!=index['canonical_git_blob']:
        raise ValueError('canonical plan integrity mismatch')
    return plan


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--assemble',required=True)
    parser.add_argument('--output',required=True)
    args=parser.parse_args()
    Path(args.output).write_bytes(canonical(read_plan(args.assemble)))
