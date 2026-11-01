#!/usr/bin/env python3
"""Render a minimal five-step blue GIF. Requires Pillow and ffmpeg."""

import argparse
from functools import lru_cache
from pathlib import Path
import subprocess
import tempfile

from PIL import Image, ImageFilter, ImageFont

from render_architecture import ACCENT, ACCENT_RGB, BG, Canvas, PALE, ROOT, SCALE, WHITE, font


@lru_cache(maxsize=None)
def chinese_font(size, bold=False):
    paths = [
        '/System/Library/Fonts/Hiragino Sans GB.ttc',
        '/System/Library/Fonts/STHeiti Medium.ttc',
        '/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc',
    ]
    for path in paths:
        if Path(path).exists():
            return ImageFont.truetype(path, round(size*SCALE), index=0)
    raise RuntimeError('A Chinese font (Hiragino Sans GB or Noto Sans CJK) is required.')


class FlowCanvas(Canvas):
    def text(self,x,y,text,size=20,color=WHITE,mono=False,bold=False,anchor='la'):
        face=font(size,True,bold) if mono else chinese_font(size,bold)
        self.d.text((round(x*SCALE),round(y*SCALE)),text,font=face,fill=color,anchor=anchor)


W, H, FPS, DURATION = 1500, 400, 20, 8
STEPS = [
    ('让 Agent', '接入 MCP'),
    ('让Agent', '生成连接命令'),
    ('分享连接命令', ''),
    ('对方执行', '授权连接'),
    ('Agent开始', '远程协助'),
]
STEPS_EN = [
    ('Connect Agent', 'to MCP'),
    ('Ask Agent for a', 'connection command'),
    ('Share the', 'connection command'),
    ('Recipient runs it', 'and grants access'),
    ('Agent provides', 'remote assistance'),
]
STEP_SECONDS = 1.35


def frame(t,language='zh'):
    steps=STEPS_EN if language=='en' else STEPS
    canvas=Canvas if language=='en' else FlowCanvas
    im=Image.new('RGBA',(W*SCALE,H*SCALE),BG)
    c=canvas(im)
    c.text(60,33,'Agenterm · How to connect' if language=='en' else 'Agenterm · 使用流程',29,WHITE)
    stage=min(int(t/STEP_SECONDS),len(steps)-1)
    settled=t>=len(steps)*STEP_SECONDS
    glow=Image.new('RGBA',im.size)
    g=canvas(glow)
    ink=Image.new('RGBA',im.size)
    k=canvas(ink)
    for i,(first,second) in enumerate(steps):
        x=60+i*284
        active=i==stage or settled
        done=i<stage
        c.text(x+4,112,f'0{i+1}',15,PALE if active or done else '#5c6c80',mono=True)
        if active:
            g.rect((x,151,x+240,278),(*ACCENT_RGB,60),None,17)
        c.rect((x,151,x+240,278),'#14273d' if active else '#111b27',
               ACCENT if active else '#365b81' if done else '#2a3849',14,1.5)
        color=WHITE if active else '#b2c9df' if done else '#889bb0'
        size=21 if language=='en' else 24
        c.text(x+120,180 if second else 199,first,size,color,anchor='ma')
        if second:
            c.text(x+120,219,second,size,PALE if active else color,anchor='ma')
        if i<len(steps)-1:
            a,b=x+249,x+273
            bright=done or settled
            col='#528bc1' if bright else '#3a4d65'
            c.line([(a,214),(b,214)],col,1.6)
            c.line([(b-6,209),(b,214),(b-6,219)],col,1.6)
            local=t-i*STEP_SECONDS
            if .88<local<STEP_SECONDS:
                px=a+(b-a)*(local-.88)/(STEP_SECONDS-.88)
                g.circle(px,214,7,(*ACCENT_RGB,180))
                k.line([(a,214),(px,214)],ACCENT,2)
                k.circle(px,214,2.7,'#e5f3ff')
    for i in range(len(steps)):
        c.circle(W/2-(len(steps)-1)*8+i*16,337,2.6,ACCENT if i<=stage else '#2e3b4c')
    im=Image.alpha_composite(im,glow.filter(ImageFilter.GaussianBlur(8*SCALE)))
    im=Image.alpha_composite(im,ink)
    return im.convert('RGB').resize((W,H),Image.Resampling.LANCZOS)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--lang',choices=['zh','en'],default='zh')
    args=parser.parse_args()
    out=ROOT/f'docs/design/connection-flow-{args.lang}'
    out.parent.mkdir(parents=True,exist_ok=True)
    frame(DURATION-.5,args.lang).save(out.with_suffix('.png'))
    with tempfile.TemporaryDirectory(prefix='agenterm-simple-flow-') as temp:
        for i in range(DURATION*FPS):
            frame(i/FPS,args.lang).save(Path(temp)/f'{i:04d}.png',compress_level=1)
        subprocess.run([
            'ffmpeg','-y','-v','error','-framerate',str(FPS),'-i',str(Path(temp)/'%04d.png'),
            '-filter_complex',
            '[0:v]split[a][b];[a]palettegen=stats_mode=diff:reserve_transparent=0[p];'
            '[b][p]paletteuse=dither=bayer:bayer_scale=3:diff_mode=rectangle',
            '-loop','0',str(out.with_suffix('.gif')),
        ],check=True)
    print(out.with_suffix('.gif'))


if __name__=='__main__':
    main()
