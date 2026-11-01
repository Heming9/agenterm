#!/usr/bin/env python3
"""Render the blue architecture animation. Requires Pillow and ffmpeg; no network calls."""

from __future__ import annotations

import argparse
import math
from pathlib import Path
import random
import subprocess
import tempfile

from PIL import Image, ImageDraw, ImageFilter, ImageFont


ROOT = Path(__file__).resolve().parents[1]
W, H, SCALE = 1600, 880, 2
BG = '#0b0f14'
PANEL = '#101419'
WHITE = '#edf1f6'
MUTED = '#89909b'
WIRE = '#a1a7b2'
DIM = '#353b45'
ACCENT = '#3b9cff'
ACCENT_RGB = (59, 156, 255)
PALE = '#9bcfff'


def font(size, mono=False, bold=False):
    candidates = (
        ['/System/Library/Fonts/Menlo.ttc', '/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf']
        if mono else
        ['/System/Library/Fonts/Avenir Next.ttc', '/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf']
    )
    for path in candidates:
        if Path(path).exists():
            index = 0 if bold or mono or not path.endswith('.ttc') else 5
            return ImageFont.truetype(path, round(size*SCALE), index=index)
    raise RuntimeError('Install Avenir Next or DejaVu fonts.')


FONTS = {}


class Canvas:
    def __init__(self, im):
        self.im = im
        self.d = ImageDraw.Draw(im)

    def line(self, points, fill=WIRE, width=1.2):
        self.d.line([(round(x*SCALE), round(y*SCALE)) for x, y in points], fill=fill,
                    width=max(1, round(width*SCALE)), joint='curve')

    def poly(self, points, fill=PANEL, outline=WIRE, width=1.2):
        p = [(round(x*SCALE), round(y*SCALE)) for x, y in points]
        self.d.polygon(p, fill=fill)
        if outline:
            self.line(points + [points[0]], outline, width)

    def rect(self, box, fill=None, outline=None, radius=0, width=1):
        box = tuple(round(x*SCALE) for x in box)
        self.d.rounded_rectangle(box, round(radius*SCALE), fill=fill, outline=outline,
                                 width=max(1, round(width*SCALE)))

    def circle(self, x, y, r, fill=None, outline=None, width=1):
        self.d.ellipse(tuple(round(v*SCALE) for v in (x-r,y-r,x+r,y+r)), fill=fill,
                       outline=outline, width=max(1, round(width*SCALE)))

    def ellipse(self, box, fill=None, outline=WIRE, width=1):
        self.d.ellipse(tuple(round(v*SCALE) for v in box), fill=fill, outline=outline,
                       width=max(1, round(width*SCALE)))

    def text(self, x, y, text, size=20, color=WHITE, mono=False, bold=False, anchor='la'):
        key = (size, mono, bold)
        if key not in FONTS:
            FONTS[key] = font(size, mono, bold)
        self.d.text((round(x*SCALE),round(y*SCALE)),text, font=FONTS[key], fill=color, anchor=anchor)

    def dash(self, a, b, fill=DIM, dash=4, gap=6, width=1):
        length=math.dist(a,b)
        for s in range(0,math.ceil(length),dash+gap):
            e=min(s+dash,length)
            self.line([(a[0]+(b[0]-a[0])*s/length,a[1]+(b[1]-a[1])*s/length),
                       (a[0]+(b[0]-a[0])*e/length,a[1]+(b[1]-a[1])*e/length)],fill,width)


def iso(x, y, u, v, z=0):
    return x+(u-v)*0.86, y+(u+v)*0.45-z


def slab(c, x, y, z, size=100, thick=15, fill='#161b22'):
    a=iso(x,y,-size,-size,z); b=iso(x,y,size,-size,z)
    d=iso(x,y,-size,size,z); e=iso(x,y,size,size,z)
    low=lambda p:(p[0],p[1]+thick)
    c.poly([a,b,e,d],fill,WIRE,1.5)
    c.poly([d,e,low(e),low(d)],'#11151b',WIRE,1.5)
    c.poly([e,b,low(b),low(e)],'#0e1218',WIRE,1.5)
    return a,b,e,d


def footprint(c,x,y,size=112):
    pts=[iso(x,y,-size,-size),iso(x,y,size,-size),iso(x,y,size,size),iso(x,y,-size,size)]
    for a,b in zip(pts,pts[1:]+pts[:1]):
        c.dash(a,b,'#30353e',4,6)


def agent(c,x,y):
    footprint(c,x,y,95)
    slab(c,x,y,8,91,15)
    slab(c,x,y,32,91,15)
    slab(c,x,y,56,91,15)
    c.ellipse((x-47,y-79,x+47,y-33),None,WIRE,1.4)
    c.ellipse((x-30,y-71,x+30,y-41),None,DIM,1.3)
    c.ellipse((x-11,y-62,x+11,y-50),ACCENT,None)
    for u in [-67,-42,42,67]:
        c.line([iso(x,y,u,-91,56),iso(x,y,u,-65,56)],DIM)
        c.line([iso(x,y,u,91,56),iso(x,y,u,65,56)],DIM)
    for z in [8,32,56]:
        p=iso(x,y,67,91,z-7)
        c.line([p,(p[0]+5,p[1]+3)],ACCENT,3)


def server(c,x,y):
    footprint(c,x,y,95)
    for z in [2,31,60]:
        slab(c,x,y,z,86,18)
        for u in [-62,-50,-38]:
            p=iso(x,y,u,86,z-7)
            c.line([p,(p[0],p[1]+5)],MUTED,1)
        p=iso(x,y,61,86,z-8)
        c.line([p,(p[0]+5,p[1]+3)],ACCENT,3)
    c.ellipse((x-28,y-75,x+28,y-45),None,WIRE,1.3)
    c.ellipse((x-13,y-68,x+13,y-52),None,ACCENT,1.5)
    c.circle(x,y-60,3,ACCENT)


def window(c,x,y,terminal=False):
    # A deterministic oblique projection keeps all terminal text crisp.
    def p(u,v): return (x+u,y+u*.48+v)
    footprint(c,x,y+30,80)
    for shift in [16,8]:
        pts=[(p(u,v)[0]+shift,p(u,v)[1]-shift*.5) for u,v in [(-82,-80),(82,-80),(82,52),(-82,52)]]
        for a,b in zip(pts,pts[1:]+pts[:1]):c.dash(a,b,DIM,4,5)
    c.poly([p(-82,-80),p(82,-80),p(82,52),p(-82,52)],'#12161c',WIRE,1.6)
    c.line([p(-82,-57),p(82,-57)],DIM)
    for u in [-68,-55,-42]:
        q=p(u,-68);c.circle(*q,2.2,ACCENT if u==-68 else '#646b77')
    if terminal:
        for v in [-30,9]:
            c.line([p(-65,v-5),p(-58,v),p(-65,v+5)],ACCENT if v==-30 else MUTED,1.7)
        c.line([p(-45,-30),p(50,-30)],WIRE,1.5)
        c.line([p(-65,-12),p(18,-12)],DIM,1.4)
        c.line([p(-45,9),p(27,9)],DIM,1.4)
        c.line([p(-65,29),p(61,29)],DIM,1.4)
    else:
        c.poly([p(-65,-37),p(-15,-37),p(-15,30),p(-65,30)],'#151b23',DIM)
        for v in [-23,-8,7]: c.line([p(-56,v),p(-25,v)],MUTED,1)
        c.line([p(1,-31),p(65,-31)],WIRE,1.5)
        c.line([p(1,-14),p(46,-14)],DIM,1.5)
        c.line([p(1,3),p(59,3)],DIM,1.5)
        c.circle(*p(7,25),3.1,ACCENT)
        c.line([p(19,25),p(60,25)],ACCENT,1.5)


def static_art():
    im=Image.new('RGB',(W*SCALE,H*SCALE),BG)
    c=Canvas(im)
    c.text(54,31,'Agenterm',42,bold=True)
    c.text(1545,51,'ONE CONNECTION. REMOTE EXECUTION.',17,MUTED,mono=True,anchor='ra')
    c.text(56,93,'Your agent. Any computer. One authorized connection.',23,'#a6aeb9')
    c.rect((32,152,1568,850),PANEL,'#282e37',25,1)
    rng=random.Random(31)
    for y in range(170,839,26):
        for x in range(48,1555,26):
            c.circle(x,y,.7,('#242a32' if rng.random()>.65 else '#1b2129'))
    for x in [57,1543]:
        for y in [179,824]:
            c.line([(x-6,y),(x+6,y)],DIM)
            c.line([(x,y-6),(x,y+6)],DIM)
    c.text(81,176,'FIG 01 / SYSTEM ARCHITECTURE',14,MUTED,mono=True)
    c.circle(1340,187,3.2,ACCENT)
    c.text(1355,176,'LIVE SESSION',14,'#b7bdc6',mono=True)

    # Ownership brackets describe where each process runs.
    for a,b,label in [(100,390,'YOUR AGENT'),(474,796,'HOSTED / SELF-HOSTED'),(874,1498,'TARGET COMPUTER')]:
        cy=243
        c.line([(a,cy+5),(a,cy-5)],DIM)
        c.line([(b,cy+5),(b,cy-5)],DIM)
        c.line([(a,cy),(b,cy)],DIM)
        tw=c.d.textlength(label,font=font(13,True))/SCALE
        c.rect(((a+b)/2-tw/2-14,cy-12,(a+b)/2+tw/2+14,cy+12),PANEL)
        c.text((a+b)/2,cy-10,label,13,MUTED,mono=True,anchor='ma')

    # Render at a reduced scale so the ownership brackets have breathing room.
    for x,draw_icon in [(244,lambda c:agent(c,244,379)),
                        (634,lambda c:server(c,634,379)),
                        (1002,lambda c:window(c,1002,353)),
                        (1372,lambda c:window(c,1372,353,True))]:
        layer=Image.new('RGBA',im.size)
        draw_icon(Canvas(layer))
        crop=layer.crop(((x-210)*SCALE,200*SCALE,(x+210)*SCALE,560*SCALE))
        crop=crop.resize((round(crop.width*.76),round(crop.height*.76)),Image.Resampling.LANCZOS)
        im.paste(crop,(round((x-210*.76)*SCALE),round((350+(200-350)*.76+10)*SCALE)),crop)
    for x,title,sub in [(244,'AI Agent','MCP-compatible agent'),(634,'Server','Command & result relay'),
                        (1002,'Client','CLI / GUI'),(1372,'shell','Local execution')]:
        c.text(x,465,title,28,WHITE,bold=True,anchor='ma')
        c.text(x,505,sub,15,MUTED,anchor='ma')
        c.line([(x,544),(x,586)],DIM,1.3)

    # Two lanes make command / response directions explicit.
    for y,col in [(586,'#234b7c'),(648,'#424a57')]:
        c.line([(244,y),(1372,y)],col,1.5)
    for x in [244,634,1002,1372]:
        c.line([(x,586),(x,648)],'#303945',1)
        for y in [586,648]:
            c.circle(x,y,7,PANEL,'#646d7b',1.7)
            c.circle(x,y,2.2,'#535c69')
    for x,label in [(438,'MCP'),(818,'WebSocket'),(1185,'stdio / PTY')]:
        c.text(x,554,label,14,'#b1b6bf',mono=True,anchor='ma')
    c.text(92,575,'CMD',13,ACCENT,mono=True)
    c.line([(145,586),(163,586),(158,582)],ACCENT,1.3)
    c.text(92,637,'OUT',13,'#b2bbc9',mono=True)
    c.line([(163,648),(145,648),(150,644)],'#b2bbc9',1.3)
    c.text(1480,575,'01',12,MUTED,mono=True,anchor='ra')
    c.text(1480,637,'02',12,MUTED,mono=True,anchor='ra')

    c.line([(82,704),(1518,704)],'#2c323c')
    c.text(82,784,'MCP  /  WebSocket  /  shell',13,'#767f8c',mono=True)
    c.text(1518,784,'Outbound connection · No inbound port required',14,'#929ba8',anchor='ra')
    return im


def point_at(points,t):
    lengths=[math.dist(a,b) for a,b in zip(points,points[1:])]
    distance=max(0,min(1,t))*sum(lengths)
    for a,b,length in zip(points,points[1:],lengths):
        if distance<=length:
            f=distance/length
            return a[0]+(b[0]-a[0])*f,a[1]+(b[1]-a[1])*f
        distance-=length
    return points[-1]


def frame(base,t):
    im=base.copy();c=Canvas(im)
    glow=Image.new('RGBA',im.size);g=Canvas(glow)
    crisp=Image.new('RGBA',im.size);k=Canvas(crisp)
    pulse=(math.sin(t*math.tau/2)+1)/2
    # A single round trip per eight-second seamless loop.
    if t<4:
        points=[(244,544),(244,586),(634,586),(1002,586),(1372,586),(1372,544)]
        progress=t/4
        color=ACCENT_RGB
        step=0 if t<.85 else 1 if t<3.25 else 2
    else:
        points=[(1372,544),(1372,648),(1002,648),(634,648),(244,648),(244,544)]
        progress=(t-4)/4
        color=(218,228,244)
        step=2 if t<4.45 else 3
    for j in range(36):
        p=progress-j*.0023
        if p<0: continue
        a=point_at(points,p)
        b=point_at(points,min(progress,p+.003))
        alpha=round(230*(1-j/36)**1.5)
        g.line([a,b],(*color,alpha),7)
        k.line([a,b],(*color,alpha),2.5)
    hx,hy=point_at(points,progress)
    g.circle(hx,hy,10,(*color,210))
    k.circle(hx,hy,3.6,'#e5f3ff' if t<4 else '#f4f7ff')
    for x in [244,634,1002,1372]:
        # Subtle connected indicators and timed node echoes.
        g.circle(x,586,7,(*ACCENT_RGB,round(45+25*pulse)))
        k.circle(x,586,7,PANEL,ACCENT,1.7)
        k.circle(x,586,2.1,ACCENT)
        for y in [586,648]:
            dist=math.hypot(hx-x,hy-y)
            if dist<65:
                strength=1-dist/65
                r=9+(1-strength)*12
                k.circle(x,y,r,None,(*color,round(strength*125)),1)
    # Breathing core in the agent processor.
    g.ellipse((234,330,254,348),(*ACCENT_RGB,round(80+80*pulse)),None)
    k.ellipse((237,335,251,343),ACCENT,None)
    im=Image.alpha_composite(im.convert('RGBA'),glow.filter(ImageFilter.GaussianBlur(7*SCALE)))
    im=Image.alpha_composite(im,crisp)
    c=Canvas(im)
    # Terminal readout changes only after the command reaches the shell.
    c.rect((1240,431,1496,456),'#11171e','#333c48',5)
    if t<3.25:
        text='$ _'; textcolor='#6b7583'
    elif t<4.45:
        command='$ uname -s'
        text=command[:max(2,round((t-3.25)/.75*len(command)))]
        textcolor=PALE
    else:
        text='$ uname -s  >  Linux';textcolor='#c4cdd9'
    c.text(1254,435,text,12,textcolor,mono=True)
    labels=['01  CONNECT','02  DISPATCH','03  EXECUTE','04  RETURN']
    xs=[82,287,492,697]
    for i,(x,label) in enumerate(zip(xs,labels)):
        active=i==step
        c.rect((x,728,x+181,766),'#15253b' if active else '#131920',
               '#345b85' if active else '#2c333e',8)
        c.text(x+90,739,label,13,PALE if active else '#74808f',mono=True,anchor='ma')
    descriptions=['Session connected','Sending command','Running in local shell','Returning result']
    c.circle(1153,747,3,ACCENT if step!=3 else '#cdd9ea')
    c.text(1170,736,descriptions[step],16,'#c2c9d3')
    return im.convert('RGB').resize((W,H),Image.Resampling.LANCZOS)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out',type=Path,default=ROOT/'docs/design/architecture-blue')
    parser.add_argument('--poster-only',action='store_true')
    parser.add_argument('--fps',type=int,default=20)
    args=parser.parse_args()
    args.out.parent.mkdir(parents=True,exist_ok=True)
    base=static_art()
    poster=frame(base,2.6)
    poster.save(args.out.with_suffix('.png'))
    print(f'Poster: {args.out.with_suffix(".png")}',flush=True)
    if args.poster_only:return
    with tempfile.TemporaryDirectory(prefix='agenterm-architecture-') as temp:
        frames=Path(temp)
        for i in range(8*args.fps):
            frame(base,i/args.fps).save(frames/f'{i:04d}.png',compress_level=1)
            if i%args.fps==0:print(f'Rendered {i}/{8*args.fps} frames',flush=True)
        sequence=str(frames/'%04d.png')
        subprocess.run(['ffmpeg','-y','-v','error','-framerate',str(args.fps),'-i',sequence,
                        '-vf','format=yuv420p','-c:v','libx264','-crf','18','-movflags','+faststart',
                        '-an',str(args.out.with_suffix('.mp4'))],check=True)
        subprocess.run(['ffmpeg','-y','-v','error','-framerate',str(args.fps),'-i',sequence,
                        '-filter_complex',
                        '[0:v]split[a][b];[a]palettegen=stats_mode=diff:reserve_transparent=0[p];'
                        '[b][p]paletteuse=dither=bayer:bayer_scale=3:diff_mode=rectangle',
                        '-loop','0',str(args.out.with_suffix('.gif'))],check=True)
    print(f'GIF: {args.out.with_suffix(".gif")}',flush=True)


if __name__=='__main__':
    main()
