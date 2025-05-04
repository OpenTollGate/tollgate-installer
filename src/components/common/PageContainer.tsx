import React from 'react';
import styled from 'styled-components';
import logoWhite from '../../assets/TollGate_Logo-C-white.png';

interface PageContainerProps {
  children: React.ReactNode;
  title: string;
  subtitle?: string;
}

const Container = styled.div`
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  padding: ${props => props.theme.space.xl};
  background-color: transparent;
  position: relative;
  z-index: 1;
`;

const ContentCard = styled.div`
  background: #FFFFFF;
  box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
  padding: ${props => props.theme.space.xl};
  border-radius: ${props => props.theme.radii.lg};
  text-align: center;
  max-width: 600px;
  margin: 0 auto;
`;

const Logo = styled.img`
  width: 150px;
  height: auto;
  margin: 2rem auto;
`;

const Title = styled.h1`
  font-size: ${props => props.theme.fontSizes['3xl']};
  color: ${props => props.theme.colors.text};
  margin-bottom: ${props => props.theme.space.sm};
  text-align: center;
`;

const Subtitle = styled.p`
  font-size: ${props => props.theme.fontSizes.lg};
  color: ${props => props.theme.colors.textSecondary};
  margin-bottom: ${props => props.theme.space.xl};
  text-align: center;
  max-width: 600px;
`;

const PageContainer: React.FC<PageContainerProps> = ({ children, title, subtitle }) => {
  return (
    <Container>
      <Logo src={logoWhite} alt="TollGate Logo" />
      <ContentCard>
        <Title>{title}</Title>
        {subtitle && <Subtitle>{subtitle}</Subtitle>}
        {children}
      </ContentCard>
    </Container>
  );
};

export default PageContainer;